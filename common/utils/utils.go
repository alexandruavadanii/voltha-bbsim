/*
 * Copyright 2018-present Open Networking Foundation

 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at

 * http://www.apache.org/licenses/LICENSE-2.0

 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package utils

import (
	"fmt"

	"gerrit.opencord.org/voltha-bbsim/device"
)

func OnuToSn(onu *device.Onu) string {
	// TODO this can be more elegant,
	// see https://github.com/opencord/voltha/blob/master/voltha/adapters/openolt/openolt_device.py#L929-L943
	return string(onu.SerialNumber.VendorId) + "00000" + fmt.Sprint(onu.IntfID) + "0" + fmt.Sprintf("%x", onu.OnuID)
}
