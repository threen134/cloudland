/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include <errno.h>
#include <fcntl.h>
#include <sci.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <fstream>

#include "exception.hpp"
#include "log.hpp"
#include "netlayer.hpp"
#include "packer.hpp"

const char *REPORT_RC_CMD = "/opt/cloudland/scripts/backend/report_rc.sh";

string pidFile;
string hostList;

void backHandler(void *user_param, sci_group_t group, void *buffer, int size) {
  int rc, my_id;
  char *p = (char *)buffer;
  int ctLen = 0;
  Packer packer((char *)buffer);
  int id = packer.unpackInt();
  int extra = packer.unpackInt();
  char *control = packer.unpackStr();
  char *command = NULL;
  char *inter = strstr(control, "inter=");
  char *select = strstr(control, "select=");
  char *toall = strstr(control, "toall=");
  char *grp = strstr(control, "group=");
  char *type = strstr(control, "type=file");

  rc = SCI_Query(BACKEND_ID, &my_id);
  if (rc != SCI_SUCCESS) {
    log_error(
        "backHandler: SCI_Query BACKEND_ID failed rc=%d, dropping message", rc);
    return;
  }
  if (toall != NULL) {
    p = strstr(toall, "toall=agent");
    if (p != NULL) {
      log_info("backHandler: node %d skipping toall=agent message", my_id);
      return;
    }
  } else if (inter != NULL) {
    p = inter + strlen("inter=");
    if ((p == NULL) || (atoi(p) < 0)) {
      log_error(
          "backHandler: node %d invalid inter= value in control '%s', dropped",
          my_id, control);
      return;
    }
  }

  if (type != NULL) {
    char *filepath = packer.unpackStr();
    int filesize = packer.unpackInt();
    int checksum = packer.unpackInt();
    int fileseek = packer.unpackInt();
    int clen;
    char *content = packer.unpackStr(&clen);
    log_info("backHandler: node %d receiving file path=%s size=%d seek=%d "
             "content_len=%d",
             my_id, filepath, filesize, fileseek, clen);
    ofstream file;
    if (fileseek == 0) {
      file.open(filepath, fstream::binary | fstream::out);
    } else {
      file.open(filepath, fstream::binary | fstream::in | fstream::out);
    }
    if (!file.is_open()) {
      log_error(
          "backHandler: node %d failed to open file '%s' for writing (seek=%d)",
          my_id, filepath, fileseek);
      return;
    }
    file.seekp(fileseek);
    file.write(content, clen);
    if (file.fail()) {
      log_error("backHandler: node %d failed to write %d bytes to '%s'", my_id,
                clen, filepath);
    } else {
      log_info("backHandler: node %d wrote %d bytes to '%s' at offset %d",
               my_id, clen, filepath, fileseek);
    }
    file.close();
    return;
  }
  command = packer.unpackStr();
  if ((inter != NULL) || (toall != NULL) || (grp != NULL) || (select != NULL)) {
    int bytes = 0;
    void *bufs[1];
    int sizes[1];
    FILE *fp = NULL;
    char tmp[1024] = {0};
    int code;
    string cmdStr = command;
    char *trace = packer.unpackStr();

    log_info("backHandler: node %d executing command: %s (trace=%s)", my_id,
             command, trace);
    setenv("RequestID", trace, 1);
    cmdStr = "sudo -E " + cmdStr + " 2>&1";
    cmdStr = cmdStr + " 2>&1";
    fp = popen(cmdStr.c_str(), "r");
    if (fp == NULL) {
      log_error("backHandler: node %d popen failed for command '%s', errno=%d",
                my_id, cmdStr.c_str(), errno);
      unsetenv("RequestID");
      return;
    }
    char *p = fgets(tmp, sizeof(tmp), fp);
    while (p != NULL) {
      Packer resp;
      resp.packInt(id);
      resp.packInt(my_id);
      resp.packStr("callback");
      resp.packStr(tmp);
      resp.packStr(trace);
      bufs[0] = resp.getPackedMsg();
      sizes[0] = resp.getPackedMsgLen();
      rc = SCI_Upload(SCI_FILTER_NULL, group, 1, bufs, sizes);
      p = fgets(tmp, sizeof(tmp), fp);
    }
    rc = fclose(fp);
    code = WEXITSTATUS(rc);
    log_info("backHandler: node %d command finished with exit code=%d", my_id,
             code);
    unsetenv("RequestID");
    if (code != 0) {
      log_error("backHandler: node %d command '%s' failed with exit code=%d",
                my_id, command, code);
      Packer resp;
      resp.packInt(id);
      resp.packInt(my_id);
      resp.packStr("error");
      resp.packStr(command);
      resp.packStr(trace);
      bufs[0] = resp.getPackedMsg();
      sizes[0] = resp.getPackedMsgLen();
      rc = SCI_Upload(SCI_FILTER_NULL, group, 1, bufs, sizes);
    }
  }
}

int startBE() {
  int rc;

  log_info("Starting SCI backend (cloudlet)...");
  sci_info_t sciInfo;
  memset(&sciInfo, 0, sizeof(sciInfo));
  sciInfo.type = SCI_BACK_END;
  sciInfo.be_info.mode = SCI_INTERRUPT;
  sciInfo.be_info.hndlr = (SCI_msg_hndlr *)&backHandler;
  sciInfo.be_info.param = NULL;
  sciInfo.enable_recover = 1;

  rc = SCI_Initialize(&sciInfo);
  if (rc != SCI_SUCCESS) {
    log_error("SCI_Initialize for backend failed with rc=%d", rc);
    throw CommonException(CommonException::SCI_INIT_ERROR);
  }
  log_info("SCI backend initialized successfully");

  return 0;
}

void set_oom_adj(int s) {
  char oomfname[256] = "";
  char oomadjstr[16] = "";
  int oomfd = -1;
  struct stat oomadjst = {0};
  int nbytes = -1;

  int score = s;
  score = (score < -1000) ? (-1000) : (score);
  score = (score > 1000) ? (1000) : (score);

  ::sprintf(oomfname, "/proc/%d/oom_score_adj", getpid());
  if (::stat(oomfname, &oomadjst) != 0) {
    /* could not find oom_score_adj, will try oom_adj instead */
    ::sprintf(oomfname, "/proc/%d/oom_adj", getpid());
    double oomadjval = (score < 0) ? (((double)score / 1000.0) * 17.0)
                                   : (((double)score / 1000.0) * 15.0);
    ::sprintf(oomadjstr, "%.0f", oomadjval);
  } else {
    ::sprintf(oomadjstr, "%d", score);
  }

  oomfd = ::open(oomfname, O_WRONLY, 0);
  if (oomfd < 0) {
    log_error("open() failed for %s: errno = %d\n", oomfname, errno);
    return;
  }

  nbytes = ::write(oomfd, oomadjstr, strlen(oomadjstr));
  if (nbytes < 0) {
    log_error("write() failed for %s: errno = %d", oomfname, errno);
  } else {
    log_crit("wrote %d bytes to %s: %s", nbytes, oomfname, oomadjstr);
  }
  ::close(oomfd);
}

int main(int argc, char *argv[]) {
  int rc, bytes, myID;
  int status = 0;
  int msgID = 0;
  char result[1024] = {0};
  char ctl[16] = "report";
  FILE *fp = NULL;
  sigset_t sigs_to_block;
  sigset_t old_sigs;

  const char *logDir = getenv("SCI_LOG_DIRECTORY");
  if (logDir == NULL) {
    logDir = "/opt/cloudland/log";
  }
  Loger::getInstance()->init(logDir, "cloudlet.log", Loger::INFORMATION,
                             Loger::ENABLE);

  log_info("Cloudlet starting up, pid=%d", getpid());
  try {
    set_oom_adj(-1000);

    startBE();

    rc = SCI_Query(BACKEND_ID, &myID);
    if (rc != SCI_SUCCESS) {
      log_error(
          "SCI_Query BACKEND_ID failed with rc=%d, cannot determine node ID",
          rc);
      return -1;
    }
    log_info("Cloudlet registered as node ID=%d", myID);

    sigemptyset(&sigs_to_block);
    pthread_sigmask(SIG_SETMASK, &sigs_to_block, &old_sigs);
    setsid();

    log_info("Node %d entering health report loop, report_cmd=%s", myID,
             REPORT_RC_CMD);
    while (status == 0) {
      void *bufs[1];
      int sizes[1];
      Packer packer;
      packer.packInt(msgID);
      packer.packInt(myID);
      packer.packStr("report");
      char *p = NULL;
      memset(result, '\0', sizeof(result));
      fp = popen(REPORT_RC_CMD, "r");
      if (fp == NULL) {
        log_error("Node %d failed to run report command: %s, errno=%d", myID,
                  REPORT_RC_CMD, errno);
      } else {
        p = fgets(result, sizeof(result) - 1, fp);
        log_info("Node %d report output starts with: %s", myID,
                 result ? result : "(null)");
      }
      packer.packStr(result);
      packer.packStr("");
      bufs[0] = packer.getPackedMsg();
      sizes[0] = packer.getPackedMsgLen();
      rc = SCI_Upload(SCHEDULE_FILTER, SCI_GROUP_ALL, 1, bufs, sizes);
      if (rc != SCI_SUCCESS) {
        log_error("Node %d SCI_Upload report failed with rc=%d", myID, rc);
      }
      do {
        p = fgets(result, sizeof(result), fp);
        if (p == NULL) {
          break;
        }
        Packer resp;
        resp.packInt(msgID);
        resp.packInt(myID);
        resp.packStr("callback");
        resp.packStr(result);
        resp.packStr("");
        bufs[0] = resp.getPackedMsg();
        sizes[0] = resp.getPackedMsgLen();
        rc = SCI_Upload(SCI_FILTER_NULL, SCI_GROUP_ALL, 1, bufs, sizes);
      } while (true);
      if (fp != NULL) {
        pclose(fp);
      }
      rc = SCI_Query(HEALTH_STATUS, &status);
      if (rc != SCI_SUCCESS) {
        log_error("Node %d SCI_Query HEALTH_STATUS failed with rc=%d", myID,
                  rc);
      } else {
        log_info("Node %d health status updated: %d", myID, status);
      }
      sleep(random() % 20 + 1);
    }
    log_info("Node %d exiting health loop with status=%d", myID, status);
    SCI_Terminate();
    log_info("Cloudlet terminated normally");
  } catch (const CommonException &e) {
    log_crit("Cloudlet caught CommonException: %s", e.getErrMsg());
    return -1;
  } catch (const std::exception &e) {
    log_crit("Cloudlet caught std::exception: %s", e.what());
    return -1;
  } catch (...) {
    log_crit("Cloudlet caught unknown exception");
    return -1;
  }

  return 0;
}
