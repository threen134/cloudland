/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include "remotexec/remotexec.grpc.pb.h"
#include "remotexec/remotexec.pb.h"
#include <grpcpp/grpcpp.h>
#include <iostream>
#include <memory>
#include <string>
#include <sys/time.h>

class FrontBack {
public:
  FrontBack(std::shared_ptr<grpc::Channel> channel)
      : stub_(com::ibm::cloudland::scripts::RemoteExec::NewStub(channel)) {}
  std::string Execute(int msg_id, int extra, const char *ctl, const char *cmd,
                      const char *trace) {
    com::ibm::cloudland::scripts::ExecuteRequest request;
    com::ibm::cloudland::scripts::ExecuteReply reply;
    grpc::ClientContext context;

    request.set_id(msg_id);
    request.set_extra(extra);
    request.set_control(ctl);
    request.set_command(cmd);

    grpc::Status status = stub_->Execute(&context, request, &reply);
    if (status.ok()) {
      return reply.status();
    } else {
      return std::string("RPC failed");
    }
  }

private:
  std::unique_ptr<com::ibm::cloudland::scripts::RemoteExec::Stub> stub_;
};

int main(int argc, char **argv) {
  if (argc < 3) {
    std::cout << argv[0] << " <extra> <control> [command]" << std::endl;
    exit(0);
  }
  int extra = atoi(argv[1]);
  const char *ctl = argv[2];
  const char *cmd = "";
  if (argc >= 4) {
    cmd = argv[3];
  }
  int msg_id = ::time(NULL);
  std::string endpoint = "localhost:50051";
  char *envp = getenv("GRPC_CLIENT_ENDPOINT");
  if (envp != NULL) {
    endpoint = envp;
  }
  FrontBack client(
      grpc::CreateChannel(endpoint, grpc::InsecureChannelCredentials()));
  //    for (i = 0; i < 10000; i++) {
  std::string reply = client.Execute(msg_id, extra, ctl, cmd, "");
  std::cout << "Remote received: " << reply << std::endl;
  //    }

  return 0;
}
