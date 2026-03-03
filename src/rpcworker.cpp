/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include <unistd.h>

#include "log.hpp"
#include "packer.hpp"
#include "rpcworker.hpp"

#include <jsoncpp/json/json.h>
#include <thread>

#define CLOUDLET_PATH "/opt/cloudland/bin/cloudlet"
#define CLOUD_HOST_FILE "/opt/cloudland/etc/host.list"
#define RPC_SERVER_ENDPOINT "0.0.0.0:5006"
#define RPC_REMOTE_ENDPOINT "localhost:5005"

#include "httplib.h"

/*
Status RemoteExecServiceImpl::Transmit(ServerContext* context,
ServerReader<FileChunk>* reader, TransmitAck* ack)
{
    FileChunk chunk;
    bool first = true;
    string control;
    int node = -1;
    char *group = NULL;
    while (reader->Read(&chunk)) {
        if (first) {
            control = chunk.control();
            char *inter = strstr((char *)control.c_str(), "inter=");
            char *toall = strstr((char *)control.c_str(), "toall=");
            if (inter != NULL) {
                char *pnode = inter + strlen("inter=");

                if ((*pnode != '\0') && (*pnode != ' ')) {
                    node = atoi((const char *)pnode);
                }
                if (node < 0) {
                    ack->set_status("Invalid node");
                    return Status::OK;
                }
            } else if (toall != NULL) {
                char *name = toall + strlen("toall=");
                char *p = strchr(name, ' ');
                if (p != NULL) {
                    *p = '\0';
                }
                if (strcmp(name, "agent") == 0) {
                    ack->set_status("Invalid group");
                    return Status::OK;
                }
                group = name;
            } else {
                ack->set_status("Not supported");
                return Status::OK;
            }

            log_info("Transmit File: %d, control: %s, path: %s, size %lld",
chunk.id(), (char *)control.c_str(), (char *)chunk.filepath().c_str(),
chunk.filesize()); if (control.find("type=") == string::npos) { control += "
type=file";
            }
            first = false;
        }
        Packer packer;
        packer.packInt(chunk.id());
        packer.packInt(chunk.extra());
        packer.packStr(control);
        packer.packStr(chunk.filepath());
        packer.packInt(chunk.filesize());
        packer.packInt(chunk.checksum());
        packer.packInt(chunk.fileseek());
        packer.packStr(chunk.content(), chunk.content().size());
        char *message = packer.getPackedMsg();
        int length = packer.getPackedMsgLen();
        if (node >= 0) {
            sciNet.sendMessage(node, message, length);
        } else if (group != NULL) {
            sciNet.sendMessage(message, length, group, false);
        }
    }
    ack->set_status("OK");
    return Status::OK;
}
*/

int RemoteExecServiceImpl::exec_cmd(int id, char *cmd) {
  int bytes = 0;
  FILE *fp = NULL;
  char result[1024] = {0};
  char command[1024] = {0};

  snprintf(command, sizeof(command), "%s %s", cmd, "2>&1");
  fp = popen(command, "r");
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

void RemoteExecServiceImpl::Execute(const Request &request,
                                    Response &response) {
  log_info("Received HTTP request from %s:%d path: %s",
           request.remote_addr.c_str(), request.remote_port,
           request.path.c_str());
  Json::Reader reader;
  Json::Value value;
  if (!reader.parse(request.body, value)) {
    log_error("Failed to parse request body: %s", request.body.c_str());
    response.status = BadRequest_400;
    response.set_content(R"({"error": "fail to parse"})", "application/json");
    return;
  }
  string trace = request.get_header_value("RequestID");
  int msgID = value["Id"].asInt();
  int extra = value["Extra"].asInt();
  char *control = strdup(value["Control"].asString().c_str());
  char *command = strdup(value["Command"].asString().c_str());
  bool replied = false;
  Packer packer;
  packer.packInt(msgID);
  packer.packInt(extra);
  packer.packStr(control);
  packer.packStr(command);
  char *inter = strstr(control, "inter=");
  char *select = strstr(control, "select=");
  char *group = strstr(control, "group=");
  char *toall = strstr(control, "toall=");
  char *mkgrp = strstr(control, "mkgrp=");
  char *rmgrp = strstr(control, "rmgrp=");
  char *lsgrp = strstr(control, "lsgrp=");
  char *term = strstr(control, "term=");
  char *callback = strstr(control, "callback");
  char *message = packer.getPackedMsg();
  int length = packer.getPackedMsgLen();

  log_info("Received message id: %d, extra: %d, control: %s, command: %s",
           msgID, extra, control, command);
  try {
    if (inter != NULL) {
      int node = -1;
      char *pnode = inter + strlen("inter=");

      if ((*pnode != '\0') && (*pnode != ' ')) {
        node = atoi((const char *)pnode);
      }
      if (node >= 0) {
        log_info(
            "API: Direct node execution. Node: %d, Message ID: %d, Command: %s",
            node, msgID, command);
        sciNet.sendMessage(node, message, length);
      } else {
        log_info("API: Scheduler execution. Message ID: %d, Command: %s", msgID,
                 command);
        sciNet.sendMessage(message, length);
      }
    } else if (group != NULL) {
      char *name = group + strlen("group=");
      char *p = strchr(group, ' ');
      if (p != NULL) {
        *p = '\0';
      }
      log_info("API: Group execution. Group: %s, Message ID: %d, Command: %s",
               name, msgID, command);
      sciNet.sendMessage(message, length, name);
    } else if (select != NULL) {
      char *desc = select + strlen("select=");
      log_info("API: Group selection execution. Selection: %s, Message ID: %d, "
               "Command: %s",
               desc, msgID, command);
      sciNet.createGroup(desc);
      char *p = strchr(select, ':');
      if (p != NULL) {
        *p = '\0';
      }
      char *name = desc;
      sciNet.sendMessage(message, length, name, true);
    } else if (toall != NULL) {
      char *name = toall + strlen("toall=");
      char *p = strchr(name, ' ');
      if (p != NULL) {
        *p = '\0';
      }
      log_info(
          "API: Broadcast execution. Target: %s, Message ID: %d, Command: %s",
          name, msgID, command);
      if (strcmp(name, "agent") == 0) {
        sciNet.sendMessage(message, length, name);
      } else {
        sciNet.groupMessage(message, length, name);
      }
    } else if (mkgrp != NULL) {
      char *desc = mkgrp + strlen("mkgrp=");
      log_info("API: Create group. Description: %s", desc);
      sciNet.createGroup(desc);
    } else if (rmgrp != NULL) {
      char *name = rmgrp + strlen("rmgrp=");
      log_info("API: Remove group. Name: %s", name);
      sciNet.freeGroup(name);
    } else if (lsgrp != NULL) {
      log_info("API: List groups");
      string groupStr = sciNet.listGroup();
      string content = R"({"groups": )" + groupStr + "}";
      response.set_content(content, "application/json");
      replied = true;
    } else if (term != NULL) {
      log_info("API: Termination request received");
      running = false;
    } else if (callback != NULL) {
      log_info("API: Local callback execution. Extra: %d, Command: %s", extra,
               command);
      exec_cmd(extra, command);
    } else {
      log_error("Unrecognized control field, rejected: %s", control);
      response.status = NotFound_404;
      response.set_content(R"({"status": "Not Found"})", "application/json");
      replied = true;
    }
  } catch (const CommonException &e) {
    log_error("CommonException in Execute: %s", e.getErrMsg());
  } catch (const std::exception &e) {
    log_error("std::exception in Execute: %s", e.what());
  } catch (...) {
    log_error("Unknown exception in Execute");
  }
  free(control);
  free(command);
  if (!replied) {
    response.set_content(R"({"status": "OK"})", "application/json");
  }
}

FrontBack::FrontBack(string rHost, int rPort)
    : remoteHost(rHost), remotePort(rPort) {}

void FrontBack::ExecuteAsync(int msg_id, int extra, char *ctl, char *cmd,
                             char *trace) {
  ctl = strdup(ctl);
  cmd = strdup(cmd);
  trace = strdup(trace);
  threadpool.AddJob([this, msg_id, extra, ctl, cmd, trace]() {
    string reply = this->Execute(msg_id, extra, ctl, cmd, trace);
    free(ctl);
    free(cmd);
    free(trace);
    log_info("rpc_replied %s", reply.c_str());
  });
}

string FrontBack::Execute(int msg_id, int extra, char *ctl, char *cmd,
                          char *trace) {
  log_info("Forwarding to backend %s:%d msg_id: %d ctl: %s cmd: %s",
           remoteHost.c_str(), remotePort, msg_id, ctl, cmd);
  Json::Value content;
  Json::StreamWriterBuilder writerBuilder;
  ostringstream cstream;
  unique_ptr<Json::StreamWriter> jsonWriter(writerBuilder.newStreamWriter());

  content["id"] = msg_id;
  content["extra"] = extra;
  content["control"] = ctl;
  content["command"] = cmd;
  jsonWriter->write(content, &cstream);

  httplib::Client cli(remoteHost, remotePort);
  //    cli.enable_server_certificate_verification(false);
  auto res = cli.Post("/internal/execute", cstream.str(), "application/json");
  if (res.error() != httplib::Error::Success) {
    string errMsg = "Failed to call RPC";
    log_info("Failed to call RPC, status: %d", res.error());
    return errMsg;
  }
  Json::Reader reader;
  Json::Value value;
  if ((res->status != OK_200) || (!reader.parse(res->body, value))) {
    string errMsg = "Failed to parse response body";
    log_info("Failed to parse response body, status: %d, body: %s", res->status,
             res->body);
    return errMsg;
  }
  string status = value["status"].asString();
  return status;
}

RemoteExecServiceImpl::RemoteExecServiceImpl(NetLayer &sci)
    : sciNet(sci), running(true) {}

RpcWorker::RpcWorker() : rpcClient(NULL) { initConn(); }

void RpcWorker::initConn() {
  char *envp = getenv("RPC_REMOTE_ENDPOINT");
  if (envp == NULL) {
    envp = RPC_REMOTE_ENDPOINT;
    log_info("RPC_REMOTE_ENDPOINT not set, using default: %s", envp);
  } else {
    log_info("RPC_REMOTE_ENDPOINT from env: %s", envp);
  }
  if (rpcClient != NULL) {
    delete rpcClient;
  }
  char *endpoint = strdup(envp);
  string remoteHost = "localhost";
  int remotePort = 50050;
  char *token = strchr(endpoint, ':');
  if (token != NULL) {
    *token = '\0';
    remoteHost = endpoint;
    remotePort = atoi(token + 1);
  }

  log_info("Connecting to backend RPC at %s:%d", remoteHost.c_str(),
           remotePort);
  rpcClient = new FrontBack(remoteHost, remotePort);
  free(endpoint);
  log_info("RPC connection initialized");
}

RpcWorker::~RpcWorker() {
  if (rpcClient != NULL) {
    delete rpcClient;
  }
}

void RpcWorker::runServer() {
  char *bePath = getenv("CLOUDLET_PATH");
  char *hFile = getenv("CLOUD_HOST_FILE");
  if (bePath == NULL) {
    bePath = CLOUDLET_PATH;
    log_info("CLOUDLET_PATH not set, using default: %s", bePath);
  } else {
    log_info("CLOUDLET_PATH from env: %s", bePath);
  }
  if (hFile == NULL) {
    hFile = CLOUD_HOST_FILE;
    log_info("CLOUD_HOST_FILE not set, using default: %s", hFile);
  } else {
    log_info("CLOUD_HOST_FILE from env: %s", hFile);
  }

  string serverAddress = "localhost";
  int serverPort = 50051;
  char *sAddr = getenv("RPC_SERVER_ENDPOINT");
  if (sAddr == NULL) {
    sAddr = RPC_SERVER_ENDPOINT;
    log_info("RPC_SERVER_ENDPOINT not set, using default: %s", sAddr);
  } else {
    log_info("RPC_SERVER_ENDPOINT from env: %s", sAddr);
  }
  char *endpoint = strdup(sAddr);
  char *token = strchr(endpoint, ':');
  if (token != NULL) {
    *token = '\0';
    serverAddress = endpoint;
    serverPort = atoi(token + 1);
  }
  free(endpoint);

  log_info("Starting HTTP RPC server on %s:%d", serverAddress.c_str(),
           serverPort);
  Server http;
  RemoteExecServiceImpl *service = new RemoteExecServiceImpl(sciNet);
  http.Post("/internal/execute", [service](const Request &req, Response &res) {
    service->Execute(req, res);
  });
  http.set_error_handler([](const Request &req, Response &res) {
    log_error("HTTP server error: status=%d path=%s remote=%s:%d", res.status,
              req.path.c_str(), req.remote_addr.c_str(), req.remote_port);
  });
  auto httpThread = std::thread([&]() {
    log_info("HTTP RPC server thread started, listening on %s:%d",
             serverAddress.c_str(), serverPort);
    bool ok = http.listen(serverAddress, serverPort);
    if (!ok) {
      log_error("HTTP RPC server failed to listen on %s:%d",
                serverAddress.c_str(), serverPort);
    }
  });

  log_info("Initializing SCI frontend with backend=%s hostfile=%s", bePath,
           hFile);
  try {
    sciNet.initFE(bePath, hFile, this);
    log_info("SCI frontend initialized successfully");
  } catch (const CommonException &e) {
    log_error("SCI frontend initialization failed (CommonException): %s",
              e.getErrMsg());
    fprintf(stderr, "[CLAND] SCI frontend initialization failed: %s\n",
            e.getErrMsg());
    http.stop();
    if (httpThread.joinable()) httpThread.join();
    throw;
  } catch (const std::exception &e) {
    log_error("SCI frontend initialization failed (std::exception): %s",
              e.what());
    fprintf(stderr, "[CLAND] SCI frontend initialization failed: %s\n",
            e.what());
    http.stop();
    if (httpThread.joinable()) httpThread.join();
    throw;
  } catch (...) {
    log_error("SCI frontend initialization failed (Unknown exception)");
    fprintf(stderr, "[CLAND] SCI frontend initialization failed: unknown\n");
    http.stop();
    if (httpThread.joinable()) httpThread.join();
    throw;
  }

  log_info(
      "RPC Worker main loop running. Waiting for HTTP thread to finish...");
  httpThread.join();
  sciNet.terminate();
  log_info("RPC worker terminated normally");
}
