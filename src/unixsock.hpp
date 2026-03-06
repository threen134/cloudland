/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#ifndef _UNIXSOCK_HPP
#define _UNIXSOCK_HPP

#include <cstring>

#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include "common/command.hpp"
#include "common/exception.hpp"
#include "common/packer.hpp"

#define CLOUDLAND_SOCK_PATH "/tmp/cloudland.sock"

class UnixSocket {
public:
  UnixSocket() : fd_(-1) {
    fd_ = ::socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd_ < 0) {
      throw CommonException(CommonException::SOCK_BIND_ERROR);
    }

    struct sockaddr_un addr;
    ::memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    ::strncpy(addr.sun_path, CLOUDLAND_SOCK_PATH, sizeof(addr.sun_path) - 1);

    if (::connect(fd_, reinterpret_cast<struct sockaddr *>(&addr),
                  sizeof(addr)) < 0) {
      ::close(fd_);
      fd_ = -1;
      throw CommonException(CommonException::SOCK_BIND_ERROR);
    }
  }

  ~UnixSocket() {
    if (fd_ >= 0) {
      ::close(fd_);
    }
  }

  /* Serialize and send a Command over the UNIX socket. */
  void send(const Command &cmd) {
    Packer packer;
    packer.packStr(cmd.control);
    packer.packStr(cmd.type);
    packer.packInt(cmd.size);
    if (cmd.size > 0 && cmd.content != nullptr) {
      packer.packStr(cmd.content, cmd.size);
    }

    char *msg = packer.getPackedMsg();
    int length = packer.getPackedMsgLen();

    int sent = 0;
    while (sent < length) {
      int n = ::write(fd_, msg + sent, length - sent);
      if (n < 0) {
        throw CommonException(CommonException::SOCK_SEND_ERROR);
      }
      sent += n;
    }
  }

private:
  int fd_;

  /* Non-copyable */
  UnixSocket(const UnixSocket &) = delete;
  UnixSocket &operator=(const UnixSocket &) = delete;
};

#endif /* _UNIXSOCK_HPP */
