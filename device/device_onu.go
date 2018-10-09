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

package device

import (
	"fmt"
	"log"
	"gerrit.opencord.org/voltha-bbsim/protos"
	"reflect"
)

type onuState int

const (
	ONU_PRE_ACTIVATED onuState = iota
	ONU_ACTIVATED
)

type Onu struct {
	InternalState onuState
	IntfID        uint32
	OperState     string
	SerialNumber  *openolt.SerialNumber
	OnuID         uint32
}

func createSN(oltid uint32, intfid uint32, onuid uint32) string {
	sn := fmt.Sprintf("%X%X%02X", oltid, intfid, onuid)
	return sn
}

func CreateOnus(oltid uint32, intfid uint32, nonus uint32, nnni uint32) []*Onu {
	onus := []*Onu{}
	for i := 0; i < int(nonus); i++ {
		onu := Onu{}
		onu.InternalState = ONU_PRE_ACTIVATED
		onu.IntfID = intfid
		onu.OperState = "up"
		onu.SerialNumber = new(openolt.SerialNumber)
		onu.SerialNumber.VendorId = []byte("NONE")
		onu.SerialNumber.VendorSpecific = []byte(createSN(oltid, intfid, uint32(i))) //FIX
		onus = append(onus, &onu)
	}
	return onus
}

func ValidateONU(targetonu openolt.Onu, regonus map[uint32][]*Onu) bool {
	for _, onus := range regonus {
		for _, onu := range onus {
			if ValidateSN(*targetonu.SerialNumber, *onu.SerialNumber) {
				return true
			}
		}
	}
	return false
}

func ValidateSN(sn1 openolt.SerialNumber, sn2 openolt.SerialNumber) bool {
	return reflect.DeepEqual(sn1.VendorId, sn2.VendorId) && reflect.DeepEqual(sn1.VendorSpecific, sn2.VendorSpecific)
}

func UpdateOnusOpStatus(ponif uint32, onus []*Onu, opstatus string) {
	for i, onu := range onus {
		onu.OperState = "up"
		log.Printf("(PONIF:%d) ONU [%d] discovered.\n", ponif, i)
	}
}
