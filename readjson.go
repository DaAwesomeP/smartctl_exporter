// Copyright 2022 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"io/ioutil"
	"os/exec"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/tidwall/gjson"
)

// JSONCache caching json
type JSONCache struct {
	JSON        gjson.Result
	LastCollect time.Time
}

var (
	jsonCache map[string]JSONCache
)

func init() {
	jsonCache = make(map[string]JSONCache)
}

// Parse json to gjson object
func parseJSON(data string) gjson.Result {
	if !gjson.Valid(data) {
		return gjson.Parse("{}")
	}
	return gjson.Parse(data)
}

// Reading fake smartctl json
func readFakeSMARTctl(logger log.Logger, device string) gjson.Result {
	s := strings.Split(device, "/")
	filename := fmt.Sprintf("debug/%s.json", s[len(s)-1])
	level.Debug(logger).Log("msg", "Read fake S.M.A.R.T. data from json", "filename", filename)
	jsonFile, err := ioutil.ReadFile(filename)
	if err != nil {
		level.Error(logger).Log("msg", "Fake S.M.A.R.T. data reading error", "err", err)
		return parseJSON("{}")
	}
	return parseJSON(string(jsonFile))
}

// Get json from smartctl and parse it
func readSMARTctl(logger log.Logger, device string) (gjson.Result, bool) {
	level.Debug(logger).Log("msg", "Collecting S.M.A.R.T. counters", "device", device)
	out, err := exec.Command(*smartctlPath, "--json", "--info", "--health", "--attributes", "--tolerance=verypermissive", "--nocheck=standby", "--format=brief", device).Output()
	if err != nil {
		level.Warn(logger).Log("msg", "S.M.A.R.T. output reading", "err", err)
	}
	json := parseJSON(string(out))
	rcOk := resultCodeIsOk(logger, json.Get("smartctl.exit_status").Int())
	jsonOk := jsonIsOk(logger, json)
	return json, rcOk && jsonOk
}

func readSMARTctlDevices(logger log.Logger) gjson.Result {
	level.Debug(logger).Log("msg", "Scanning for devices")
	out, err := exec.Command(*smartctlPath, "--json", "--scan").Output()
	if exiterr, ok := err.(*exec.ExitError); ok {
		level.Debug(logger).Log("msg", "Exit Status", "exit_code", exiterr.ExitCode())
		// The smartctl command returns 2 if devices are sleeping, ignore this error.
		if exiterr.ExitCode() != 2 {
			level.Warn(logger).Log("msg", "S.M.A.R.T. output reading error", "err", err)
			return gjson.Result{}
		}
	}
	return parseJSON(string(out))
}

// Select json source and parse
func readData(logger log.Logger, device string) (gjson.Result, error) {
	if *smartctlFakeData {
		return readFakeSMARTctl(logger, device), nil
	}

	cacheValue, cacheOk := jsonCache[device]
	if !cacheOk || time.Now().After(cacheValue.LastCollect.Add(*smartctlInterval)) {
		json, ok := readSMARTctl(logger, device)
		if ok {
			jsonCache[device] = JSONCache{JSON: json, LastCollect: time.Now()}
			return jsonCache[device].JSON, nil
		}
		return gjson.Parse("{}"), fmt.Errorf("smartctl returned bad data for device %s", device)
	}
	return cacheValue.JSON, nil
}

// Parse smartctl return code
func resultCodeIsOk(logger log.Logger, SMARTCtlResult int64) bool {
	result := true
	if SMARTCtlResult > 0 {
		b := SMARTCtlResult
		if (b & 1) != 0 {
			level.Error(logger).Log("msg", "Command line did not parse.")
			result = false
		}
		if (b & (1 << 1)) != 0 {
			level.Error(logger).Log("msg", "Device open failed, device did not return an IDENTIFY DEVICE structure, or device is in a low-power mode")
			result = false
		}
		if (b & (1 << 2)) != 0 {
			level.Warn(logger).Log("msg", "Some SMART or other ATA command to the disk failed, or there was a checksum error in a SMART data structure")
		}
		if (b & (1 << 3)) != 0 {
			level.Warn(logger).Log("msg", "SMART status check returned 'DISK FAILING'.")
		}
		if (b & (1 << 4)) != 0 {
			level.Warn(logger).Log("msg", "We found prefail Attributes <= threshold.")
		}
		if (b & (1 << 5)) != 0 {
			level.Warn(logger).Log("msg", "SMART status check returned 'DISK OK' but we found that some (usage or prefail) Attributes have been <= threshold at some time in the past.")
		}
		if (b & (1 << 6)) != 0 {
			level.Warn(logger).Log("msg", "The device error log contains records of errors.")
		}
		if (b & (1 << 7)) != 0 {
			level.Warn(logger).Log("msg", "The device self-test log contains records of errors. [ATA only] Failed self-tests outdated by a newer successful extended self-test are ignored.")
		}
	}
	return result
}

// Check json
func jsonIsOk(logger log.Logger, json gjson.Result) bool {
	messages := json.Get("smartctl.messages")
	// logger.Debug(messages.String())
	if messages.Exists() {
		for _, message := range messages.Array() {
			if message.Get("severity").String() == "error" {
				level.Error(logger).Log("msg", message.Get("string").String())
				return false
			}
		}
	}
	return true
}
