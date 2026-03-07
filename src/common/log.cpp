/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

#include <assert.h>
#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "log.hpp"

const char *logHeaderNames[] = {
    "CRITICAL", "ERROR",       "WARNING", "INFORMATION",
    "DEBUG",    "PERFORMANCE", "OTHER",
};

Loger *Loger::logger = NULL;

Loger::Loger() : useStdout(false), useJson(false) {
  pthread_mutex_init(&lock, NULL);
}

Loger::~Loger() { pthread_mutex_destroy(&lock); }

void Loger::init(const char *directory, const char *filename, int level,
                 int m) {
  assert(filename);

  pthread_mutex_lock(&lock);

  char node[256] = {0};
  gethostname(node, sizeof(node));

  logFile = filename;

  // Docker 模式检测：如果目录为空、为 NULL 或显式指定为 stdout
  if (directory == NULL || strlen(directory) == 0 ||
      strcmp(directory, "stdout") == 0) {
    useStdout = true;
    useJson = true; // Docker 下默认开启 JSON
    logDir = "stdout";
  } else {
    useStdout = false;
    logDir = directory;
    sprintf(logPath, "%s/%s.%d.%s", directory, node, (int)getpid(), filename);
    unlink(logPath);
  }

  // 环境覆盖：如果强制指定 JSON 格式
  const char *jsonEnv = getenv("SCI_LOG_FORMAT");
  if (jsonEnv != NULL && strcmp(jsonEnv, "json") == 0) {
    useJson = true;
  }

  permitLevel = level;
  mode = m;

  pthread_mutex_unlock(&lock);

  if (!useStdout) {
    log_info("Logger initialized at %s with level %d and mode %d", logPath,
             level, m);
  } else {
    log_info("Logger initialized to stdout (JSON) with level %d", level);
  }
}

void Loger::rename(const char *directory, int level, int m) {
  if (useStdout)
    return; // Stdout 模式不支持重命名文件

  pthread_mutex_lock(&lock);
  int rc = -1;
  char new_logPath[2 * MAX_PATH_LEN];
  char node[256] = {0};

  if ((level >= 0) && (permitLevel != level)) {
    permitLevel = level;
  }
  if (m != INVALID)
    mode = m;

  if (directory == NULL) {
    pthread_mutex_unlock(&lock);
    return;
  }

  if (logDir == string(directory)) {
    pthread_mutex_unlock(&lock);
    return;
  }

  gethostname(node, sizeof(node));
  sprintf(new_logPath, "%s/%s.%s.%d", directory, node, logFile.c_str(),
          (int)getpid());
  if (::access(logPath, F_OK) == 0) {
    rc = ::rename(logPath, new_logPath);
    if (rc != 0) {
      fprintf(
          stderr,
          "Unable to rename log file from %s to %s, rc is %d, errno=%d(%s)\n",
          logPath, new_logPath, rc, errno, strerror(errno));
    } else {
      sprintf(logPath, "%s", new_logPath);
      logDir = directory;
    }
  } else {
    sprintf(logPath, "%s", new_logPath);
    logDir = directory;
  }
  pthread_mutex_unlock(&lock);
}

static void escapeJsonString(const char *input, char *output, size_t maxLen) {
  size_t j = 0;
  for (size_t i = 0; input[i] != '\0' && j + 2 < maxLen; i++) {
    switch (input[i]) {
    case '"':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = '"';
      }
      break;
    case '\\':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = '\\';
      }
      break;
    case '\b':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = 'b';
      }
      break;
    case '\f':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = 'f';
      }
      break;
    case '\n':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = 'n';
      }
      break;
    case '\r':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = 'r';
      }
      break;
    case '\t':
      if (j + 2 < maxLen) {
        output[j++] = '\\';
        output[j++] = 't';
      }
      break;
    default:
      if ((unsigned char)input[i] < 32) {
        if (j + 6 < maxLen) {
          j += sprintf(&output[j], "\\u%04x", input[i]);
        }
      } else {
        output[j++] = input[i];
      }
      break;
    }
  }
  output[j] = '\0';
}

void Loger::print(int level, const char *srcFile, int srcLine,
                  const char *format, ...) {
  if (mode != ENABLE)
    return;

  if (level > permitLevel)
    return;

  va_list args;
  struct timespec ts;
  clock_gettime(CLOCK_REALTIME, &ts);
  struct tm tm_info;
  localtime_r(&ts.tv_sec, &tm_info);

  pthread_mutex_lock(&lock);

  if (useJson) {
    char rawMsg[MAX_LOG_LEN];
    char escapedMsg[MAX_LOG_LEN];
    char timeBuf[64];
    char jsonBuf[MAX_LOG_LEN + 512];

    va_start(args, format);
    vsnprintf(rawMsg, sizeof(rawMsg), format, args);
    va_end(args);

    escapeJsonString(rawMsg, escapedMsg, sizeof(escapedMsg));

    strftime(timeBuf, sizeof(timeBuf), "%Y-%m-%dT%H:%M:%S", &tm_info);
    sprintf(timeBuf + strlen(timeBuf), ".%03ldZ", ts.tv_nsec / 1000000);

    const char *requestID = getenv("RequestID");
    if (requestID == NULL)
      requestID = "-";

    const char *fileName = strrchr(srcFile, '/');
    if (fileName)
      fileName++;
    else
      fileName = srcFile;

    int len =
        snprintf(jsonBuf, sizeof(jsonBuf),
                 "{\"time\":\"%s\",\"level\":\"%s\",\"file\":\"%s\",\"line\":%"
                 "d,\"thread\":%lu,\"request_id\":\"%s\",\"message\":\"%s\"}\n",
                 timeBuf, logHeaderNames[level], fileName, srcLine,
                 (unsigned long)pthread_self(), requestID, escapedMsg);

    if (useStdout) {
      write(STDOUT_FILENO, jsonBuf, len);
    } else {
      FILE *fp = fopen(logPath, "a");
      if (fp) {
        fputs(jsonBuf, fp);
        fclose(fp);
      }
    }
  } else {
    // 传统文本格式
    char tmMsg[64];
    char content[MAX_LOG_LEN];
    strftime(tmMsg, sizeof(tmMsg), "%y%m%d-%H:%M:%S", &tm_info);

    va_start(args, format);
    vsnprintf(content, sizeof(content), format, args);
    va_end(args);

    if (useStdout) {
      fprintf(stdout, "%s [%s] %s (%s:%d|%lu)\n", tmMsg, logHeaderNames[level],
              content, srcFile, srcLine, (unsigned long)pthread_self());
    } else {
      FILE *fp = fopen(logPath, "a");
      if (fp) {
        fprintf(fp, "%s [%s] %s (%s:%d|%lu)\n", tmMsg, logHeaderNames[level],
                content, srcFile, srcLine, (unsigned long)pthread_self());
        fclose(fp);
      }
    }
  }

  pthread_mutex_unlock(&lock);
}
