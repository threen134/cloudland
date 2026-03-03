/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include <stdio.h>
#include <string.h>

#include <string>

#include "handler.hpp"
#include "log.hpp"
#include "netlayer.hpp"
#include "packer.hpp"
#include "rpcworker.hpp"

using namespace std;

const char *RESCUE_CMD = "/opt/cloudland/scripts/frontend/rescue.sh";

int exec_cmd(int id, char *cmd) {
  int bytes = 0;
  FILE *fp = NULL;
  char result[1024] = {0};
  char command[1024] = {0};

  snprintf(command, sizeof(command), "%s %s", cmd, "2>&1");
  fp = popen(cmd, "r");
  if (fp == NULL) {
    log_error("exec_cmd: popen failed for node %d cmd '%s', errno=%d", id, cmd,
              errno);
    return -1;
  }
  bytes = fread(result, sizeof(char), sizeof(result) - 1, fp);
  if (bytes == 0) {
    log_warn("exec_cmd: node %d cmd '%s' returned empty output", id, cmd);
  }
  pclose(fp);
  log_info("Backend or agent %d responded command %s, result %s", id, cmd,
           result);

  return 0;
}

void frontHandler(void *user_param, sci_group_t group, void *buffer, int size) {
  if (size < 16) {
    log_error("frontHandler: received undersized message size=%d, rejected",
              size);
    return;
  }

  Packer packer((char *)buffer);
  int msg_id = packer.unpackInt();
  int be_id = packer.unpackInt();
  char *ctl = packer.unpackStr();
  char *msg = packer.unpackStr();
  char *trace = packer.unpackStr();
  char *callback = strstr(ctl, "callback");
  char *error = strstr(ctl, "error");
  char *report = strstr(ctl, "report");
  RpcWorker *rpcWorker = (RpcWorker *)user_param;

  log_info("Cloudlet %d responded message id: %d control: %s content: %s",
           be_id, msg_id, ctl, msg);
  if (error != NULL) {
    log_info("Processing error response for msg_id: %d from be_id: %d", msg_id,
             be_id);
    rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, msg, trace);
  } else if (callback != NULL) {
    log_info("Processing callback response for msg_id: %d from be_id: %d",
             msg_id, be_id);
    char *cmd = NULL;
    char *next = NULL;
    char *tail = NULL;

    if (strstr(callback, "callback=agent") != NULL) {
      log_info("Agent callback detected, forwarding async...");
      rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, msg, trace);
      return;
    }
    cmd = strstr(msg, "|:-COMMAND-:|");
    if (cmd == NULL) {
      log_info("Direct callback forwarding async...");
      rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, msg, trace);
    } else {
      while (cmd != NULL) {
        cmd += strlen("|:-COMMAND:-|") + 1;
        next = strstr(cmd, "|:-COMMAND-:|");
        tail = strchr(cmd, '\n');
        if (tail != NULL) {
          *tail = '\0';
        }
        log_info("Extracted command from callback: %s", cmd);
        rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, cmd, trace);
        cmd = next;
      }
    }
  } else if (report != NULL) {
    if (be_id == -1) {
      log_info("Node registration report received: msg_id=%d msg=%s", msg_id,
               msg);
      rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, msg, trace);
    } else {
      log_info("Node %d sent health report (msg_id=%d), forwarding...", be_id,
               msg_id);
      rpcWorker->getClient()->ExecuteAsync(msg_id, be_id, ctl, msg, trace);
    }
  } else {
    log_error("frontHandler: unrecognized control '%s' from node %d, dropped. "
              "Trace: %s",
              ctl, be_id, trace);
  }
}
