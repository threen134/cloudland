/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include <sstream>
#include <string.h>
#include <string>

#include "exception.hpp"
#include "handler.hpp"
#include "log.hpp"
#include "netlayer.hpp"
#include "rpcworker.hpp"

using namespace std;

NetLayer::NetLayer() {
  ::pthread_mutex_init(&mtx, NULL);
  ::pthread_mutex_init(&ser, NULL);
}

NetLayer::~NetLayer() {
  groupMap.clear();
  ::pthread_mutex_destroy(&mtx);
  ::pthread_mutex_destroy(&ser);
}

void NetLayer::terminate() { SCI_Terminate(); }

void NetLayer::lock() { ::pthread_mutex_lock(&mtx); }

void NetLayer::unlock() { ::pthread_mutex_unlock(&mtx); }

void NetLayer::serialize() { ::pthread_mutex_lock(&ser); }

void NetLayer::deserialize() { ::pthread_mutex_unlock(&ser); }

int NetLayer::initFE(char *backend, RpcWorker *rpcWorker) {
  int rc;
  const char *envp = getenv("SCHEDULE_SO_FILE");
  if (envp == NULL) {
    envp = SCHEDULE_SO_FILE;
    log_info("SCHEDULE_SO_FILE not set, using default: %s", envp);
  } else {
    log_info("SCHEDULE_SO_FILE from env: %s", envp);
  }
  log_info("NetLayer initFE: backend=%s filter=%s (no hostfile, API "
           "registration mode)",
           backend, envp);

  sci_filter_info_t filter = {SCHEDULE_FILTER, const_cast<char *>(envp)};
  sci_filter_list_t flist = {1, &filter};

  bePath = backend;
  memset(&sciInfo, 0, sizeof(sciInfo));
  sciInfo.type = SCI_FRONT_END;
  sciInfo.fe_info.mode = SCI_INTERRUPT;
  sciInfo.fe_info.hostfile = NULL;
  sciInfo.fe_info.bepath = (char *)bePath.c_str();
  sciInfo.fe_info.hndlr = (SCI_msg_hndlr *)&frontHandler;
  sciInfo.fe_info.filter_list = flist;
  sciInfo.fe_info.param = rpcWorker;
  sciInfo.enable_recover = 1;

  log_info("Calling SCI_Initialize for frontend...");
  rc = SCI_Initialize(&sciInfo);
  if (rc != SCI_SUCCESS) {
    log_error("SCI_Initialize failed with rc=%d", rc);
    throw CommonException(CommonException::SCI_INIT_ERROR);
  }
  log_info("SCI_Initialize succeeded");

  return 0;
}

int NetLayer::addBackend(int beID, const char *hostname, int level) {
  sci_be_t be;
  be.id = beID;
  be.hostname = const_cast<char *>(hostname);
  be.level = level;
  int rc = SCI_BE_add(&be);
  if (rc == SCI_SUCCESS) {
    log_info("Added backend %s with ID %d", hostname, be.id);
  } else {
    log_error("Failed to add backend %s with ID %d, rc=%d", hostname, beID, rc);
  }
  return (rc == SCI_SUCCESS) ? be.id : rc;
}

int NetLayer::removeBackend(int beID) {
  int rc = SCI_BE_remove(beID);
  if (rc == SCI_SUCCESS) {
    log_info("Removed backend ID %d", beID);
  } else {
    log_error("Failed to remove backend ID %d, rc=%d", beID, rc);
  }
  return rc;
}

string NetLayer::listGroup() {
  string groupStr;
  GROUP_MAP::iterator it;

  lock();
  for (it = groupMap.begin(); it != groupMap.end(); ++it) {
    groupStr += it->second.desc + "\n";
  }
  unlock();

  return groupStr;
}

int NetLayer::freeGroup(char *grpName) {
  int rc = -1;
  log_info("Freeing group: %s", grpName);
  lock();
  if (groupMap.find(grpName) != groupMap.end()) {
    rc = SCI_Group_free(groupMap[grpName].group);
    groupMap.erase(grpName);
    log_info("Group %s freed successfully, rc=%d", grpName, rc);
  } else {
    log_warn("Group %s not found, nothing to free", grpName);
  }
  unlock();

  return rc;
}

vector<string> NetLayer::string2Array(const string &str, char splitter) {
  vector<string> tokens;
  stringstream ss(str);
  string temp;
  while (getline(ss, temp, splitter)) {
    tokens.push_back(temp);
  }
  return tokens;
}

int NetLayer::createGroup(char *grpDesc) {
  string savedDesc = grpDesc;
  char *p = strchr(grpDesc, ':');
  char *name = grpDesc;
  if (p == NULL) {
    log_error("Invalid group description (missing ':'): %s", grpDesc);
    return -1;
  }
  *p = 0;
  if (groupMap.find(name) != groupMap.end() &&
      (groupMap[name].desc == savedDesc)) {
    log_info("Group %s already exists with same description", name);
    return 0;
  }
  log_info("Creating group %s with members: %s", name, p + 1);
  vector<int> result;
  vector<string> tokens = string2Array(p + 1, ',');
  vector<string>::const_iterator it;
  for (it = tokens.begin(); it != tokens.end(); ++it) {
    const string &token = *it;
    vector<string> range = string2Array(token, '-');
    if (range.size() == 1) {
      result.push_back(atoi(range[0].c_str()));
    } else if (range.size() == 2) {
      int start = atoi(range[0].c_str());
      int stop = atoi(range[1].c_str());
      for (int j = start; j <= stop; j++) {
        result.push_back(j);
      }
    }
  }
  lock();
  groupMap[name].group = SCI_GROUP_ALL;
  groupMap[name].desc = savedDesc;
  if (result.size() > 0) {
    sci_group_t group;
    int rc = SCI_Group_create(result.size(), &result[0], &group);
    if (rc == SCI_SUCCESS) {
      groupMap[name].group = group;
      log_info("Group %s created with %lu members", name, result.size());
    } else {
      log_error("SCI_Group_create failed for %s, rc=%d", name, rc);
    }
  } else {
    log_warn("Group %s has no specific members, using SCI_GROUP_ALL", name);
  }
  unlock();

  return 0;
}

int NetLayer::sendMessage(int beID, char *message, int length) {
  int rc = -1;
  void *bufs[1];
  int sizes[1];

  bufs[0] = message;
  sizes[0] = length;
  log_info("Sending message to node %d, length=%d", beID, length);
  rc = SCI_Bcast(SCI_FILTER_NULL, beID, 1, bufs, sizes);
  if (rc != SCI_SUCCESS) {
    log_error("SCI_Bcast to node %d failed with rc=%d", beID, rc);
    throw CommonException(CommonException::SCI_BCAST_ERROR);
  }

  return 0;
}

int NetLayer::groupMessage(char *message, int length, char *grpDesc) {
  string desc = grpDesc;
  string::size_type pos = desc.find(":");
  if (pos != desc.npos) {
    string name = desc.substr(0, pos);
    serialize();
    createGroup(grpDesc);
    sendMessage(message, length, (char *)name.c_str(), false);
    freeGroup((char *)name.c_str());
    deserialize();
  } else {
    sendMessage(message, length, (char *)desc.c_str(), false);
  }

  return 0;
}

int NetLayer::sendMessage(char *message, int length, char *grpName,
                          bool useFilter) {
  int rc = -1;
  void *bufs[1];
  int sizes[1];
  int group = SCI_GROUP_ALL;
  int filter = SCHEDULE_FILTER;

  bufs[0] = message;
  sizes[0] = length;
  if (!useFilter) {
    filter = SCI_FILTER_NULL;
  }
  lock();
  if ((grpName != NULL) && (groupMap.find(grpName) != groupMap.end())) {
    group = groupMap[grpName].group;
  } else if (grpName != NULL) {
    log_error("Group '%s' not found in groupMap, sending to SCI_GROUP_ALL",
              grpName);
  }
  rc = SCI_Bcast(filter, group, 1, bufs, sizes);
  unlock();
  if (rc != SCI_SUCCESS) {
    log_error("SCI_Bcast to group '%s' failed with rc=%d",
              grpName ? grpName : "(all)", rc);
    throw CommonException(CommonException::SCI_BCAST_ERROR);
  }

  return 0;
}
