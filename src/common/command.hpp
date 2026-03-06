/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#ifndef _COMMAND_HPP
#define _COMMAND_HPP

#define CMD_CONTROL_LEN 64

struct __attribute__((__packed__)) Command {
  char control[CMD_CONTROL_LEN]; /* fixed-size control string */
  char type[CMD_CONTROL_LEN];    /* command type / subcommand   */
  int size;                      /* byte length of content      */
  char *content;                 /* heap-allocated payload      */
};

#endif
