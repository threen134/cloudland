/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package common

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

type ExecuteRequest struct {
	Id      int32
	Extra   int32
	Control string
	Command string
}

type ExecuteReply struct {
	Status string
}

var remoteExecPath string

func NodeAdd(hostname string, hostID int32) (assignedID int32, err error) {
	endpoint := viper.GetString("sci.endpoint") + "/internal/node/add"
	reqBody := map[string]interface{}{"hostname": hostname, "id": hostID, "level": 1}
	jsonReq, err := json.Marshal(reqBody)
	if err != nil {
		return -1, NewCLError(ErrExecuteOnHyperFailed, "Failed to marshal request", err)
	}
	resp, err := http.Post(endpoint, "application/json", bytes.NewBuffer(jsonReq))
	if err != nil {
		return -1, NewCLError(ErrExecuteOnHyperFailed, "Failed to add node", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return -1, NewCLError(ErrExecuteOnHyperFailed, "Failed to read response", err)
	}
	var result map[string]interface{}
	if err = json.Unmarshal(body, &result); err != nil {
		return -1, NewCLError(ErrExecuteOnHyperFailed, "Failed to parse response", err)
	}
	if resp.StatusCode != 200 {
		return -1, NewCLError(ErrExecuteOnHyperFailed, "Node add failed: "+string(body), nil)
	}
	if id, ok := result["id"].(float64); ok {
		assignedID = int32(id)
	}
	logger.Debugf("NodeAdd: hostname=%s, hostID=%d, assignedID=%d", hostname, hostID, assignedID)
	return
}

func NodeRemove(hostID int32) error {
	endpoint := viper.GetString("sci.endpoint") + "/internal/node/remove"
	reqBody := map[string]interface{}{"id": hostID}
	jsonReq, err := json.Marshal(reqBody)
	if err != nil {
		return NewCLError(ErrExecuteOnHyperFailed, "Failed to marshal request", err)
	}
	resp, err := http.Post(endpoint, "application/json", bytes.NewBuffer(jsonReq))
	if err != nil {
		return NewCLError(ErrExecuteOnHyperFailed, "Failed to remove node", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return NewCLError(ErrExecuteOnHyperFailed, "Failed to read response", err)
	}
	if resp.StatusCode != 200 {
		return NewCLError(ErrExecuteOnHyperFailed, "Node remove failed: "+string(body), nil)
	}
	logger.Debugf("NodeRemove: hostID=%d", hostID)
	return nil
}

func HyperExecute(ctx context.Context, control, command string) (err error) {
	execReq := &ExecuteRequest{
		Id:      100,
		Extra:   0,
		Control: control,
		Command: command,
	}
	jsonReq, err := json.Marshal(execReq)
	payload := bytes.NewBufferString(string(jsonReq))
	if remoteExecPath == "" {
		remoteExecPath = viper.GetString("sci.endpoint") + "/internal/execute"
	}
	logger.Debugf("remotePath: %s, jsonPayload: %v", remoteExecPath, payload)

	req, err := http.NewRequest("POST", remoteExecPath, payload)
	if err != nil {
		logger.Error("Error creating request:", err)
		return NewCLError(ErrExecuteOnHyperFailed, "Error creating request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	requestID := uuid.New().String()
	req.Header.Set("RequestID", requestID)
	logger.Debugf("Requesting RPC remotePath: %s, RequestID: %s", remoteExecPath, requestID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Error posting data:", err)
		return NewCLError(ErrExecuteOnHyperFailed, "Error posting data", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Error("Error reading response body:", err)
		return NewCLError(ErrExecuteOnHyperFailed, "Error reading response body", err)
	}

	logger.Debug("Response Status:", resp.Status)
	logger.Debug("Response Body:", string(body))
	return
}
