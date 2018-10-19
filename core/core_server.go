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

package core

import (
	"errors"
	"strconv"
	"sync"
	"time"
	"gerrit.opencord.org/voltha-bbsim/device"
	"gerrit.opencord.org/voltha-bbsim/protos"
	"gerrit.opencord.org/voltha-bbsim/common"
	"gerrit.opencord.org/voltha-bbsim/setup"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"google.golang.org/grpc"
)

type Mode int

const MAX_ONUS_PER_PON = 64 // This value should be the same with the value in AdapterPlatrorm class

const (
	DEFAULT Mode = iota
	AAA
	BOTH
)

type Server struct {
	Olt          *device.Olt
	Onumap       map[uint32][]*device.Onu
	Ioinfos      []*Ioinfo
	Endchan      chan int
	Mode         Mode
	AAAWait      int
	DhcpWait     int
	DhcpServerIP string
	Delay        int
	gRPCserver   *grpc.Server
	VethEnv      []string
	TestFlag     bool
	Processes    []string
	EnableServer *openolt.Openolt_EnableIndicationServer
	CtagMap      map[string]uint32
}

type Packet struct {
	Info *Ioinfo
	Pkt  gopacket.Packet
}

func (s *Server) Initialize() {
	s.VethEnv = []string{}
	s.Endchan = make(chan int)
	s.TestFlag = false
	s.Processes = []string{}
	s.Ioinfos = []*Ioinfo{}
}

func Create(oltid uint32, npon uint32, nonus uint32, aaawait int, dhcpwait int, ip string, delay int, g *grpc.Server, mode Mode, e chan int) *Server {
	s := new(Server)
	s.Olt = device.CreateOlt(oltid, npon, 1)
	nnni := s.Olt.NumNniIntf
	logger.Info("OLT ID: %d was retrieved.\n", s.Olt.ID)
	s.Onumap = make(map[uint32][]*device.Onu)
	s.AAAWait = aaawait
	s.DhcpWait = dhcpwait
	s.DhcpServerIP = ip
	s.gRPCserver = g
	s.Delay = delay
	s.Mode = mode
	s.Endchan = e
	s.VethEnv = []string{}
	s.TestFlag = false
	for intfid := nnni; intfid < npon+nnni; intfid++ {
		s.Onumap[intfid] = device.CreateOnus(oltid, intfid, nonus, nnni)
	}
	s.EnableServer = new(openolt.Openolt_EnableIndicationServer)

	//TODO: To be fixed
	s.CtagMap = make(map[string]uint32)
	for i := 0; i < MAX_ONUS_PER_PON; i++ {
		oltid := s.Olt.ID
		intfid := uint32(1)
		sn := convB2S(device.CreateSN(oltid, intfid, uint32(i)))
		s.CtagMap[sn] = uint32(900 + i) // This is hard coded for BBWF
	}
	return s
}

func (s *Server) activateOLT(stream openolt.Openolt_EnableIndicationServer) error {
	// Activate OLT
	olt := s.Olt
	oltid := olt.ID
	wg := &sync.WaitGroup{}

	if err := sendOltIndUp(stream, olt); err != nil {
		return err
	}
	olt.OperState = "up"
	*olt.InternalState = device.OLT_UP
	logger.Info("OLT %s sent OltInd.\n", olt.Name)

	// OLT sends Interface Indication to Adapter
	if err := sendIntfInd(stream, olt); err != nil {
		logger.Error("Fail to sendIntfInd: %v\n", err)
		return err
	}
	logger.Info("OLT %s sent IntfInd.\n", olt.Name)

	// OLT sends Operation Indication to Adapter after activating each interface
	//time.Sleep(IF_UP_TIME * time.Second)
	*olt.InternalState = device.PONIF_UP
	if err := sendOperInd(stream, olt); err != nil {
		logger.Error("Fail to sendOperInd: %v\n", err)
		return err
	}
	logger.Info("OLT %s sent OperInd.\n", olt.Name)

	// OLT sends ONU Discover Indication to Adapter after ONU discovery
	for intfid, _ := range s.Onumap {
		device.UpdateOnusOpStatus(intfid, s.Onumap[intfid], "up")
	}

	for intfid, _ := range s.Onumap {
		sendOnuDiscInd(stream, s.Onumap[intfid])
		logger.Info("OLT id:%d sent ONUDiscInd.\n", olt.ID)
	}

	// OLT Sends OnuInd after waiting all of those ONUs up
	for {
		if s.IsAllOnuActive(s.Onumap) {
			break
		}
	}

	for intfid, _ := range s.Onumap {
		sendOnuInd(stream, s.Onumap[intfid], s.Delay)
		logger.Info("OLT id:%d sent ONUInd.\n", olt.ID)
	}

	if s.Mode == DEFAULT {
		//EnableIndication's stream should be kept even after activateOLT() is finished.
		//Otherwise, OpenOLT adapter sends EnableIndication again.
		<-s.Endchan
		logger.Debug("core server thread receives close ")
	} else if s.Mode == AAA || s.Mode == BOTH {
		s.TestFlag = true
		var err error
		s.Ioinfos, s.VethEnv, err = createIoinfos(oltid, s.VethEnv, s.Onumap)
		logger.Debug("s.VethEnv:%v", s.VethEnv)
		if err != nil {
			return err
		}

		errchan := make(chan error)
		go func() {
			<-errchan
			close(s.Endchan)
		}()

		wg.Add(1)
		go func() {
			defer func() {
				logger.Debug("runPacketInDaemon Done")
				wg.Done()
			}()

			err := s.runPacketInDaemon(stream)
			if err != nil {
				errchan <- err
				return
			}
		}()

		wg.Add(1)
		go func() {
			defer func() {
				logger.Debug("exeAAATest Done")
				wg.Done()
			}()

			err = s.exeAAATest()
			if err != nil {
				errchan <- err
				return
			}

			if s.Mode == BOTH {
				go func() {
					defer func() {
						logger.Debug("exeDHCPTest Done")
					}()

					err := s.exeDHCPTest()
					if err != nil {
						errchan <- err
						return
					}
				}()
			}
		}()
		wg.Wait()
		cleanUpVeths(s.VethEnv) // Grace teardown
		pnames := s.Processes
		killProcesses(pnames)
		logger.Debug("Grace shutdown down")
	}
	return nil
}

func createIoinfos(oltid uint32, vethenv []string, onumap map[uint32][]*device.Onu) ([]*Ioinfo, []string, error) {
	ioinfos := []*Ioinfo{}
	var err error
	for intfid, _ := range onumap {
		for i := 0; i < len(onumap[intfid]); i++ {
			var handler *pcap.Handle
			onuid := onumap[intfid][i].OnuID
			uniup, unidw := makeUniName(oltid, intfid, onuid)
			if handler, vethenv, err = setupVethHandler(uniup, unidw, vethenv); err != nil {
				return ioinfos, vethenv, err
			}
			iinfo := Ioinfo{name: uniup, iotype: "uni", ioloc: "inside", intfid: intfid, onuid: onuid, handler: handler}
			ioinfos = append(ioinfos, &iinfo)
			oinfo := Ioinfo{name: unidw, iotype: "uni", ioloc: "outside", intfid: intfid, onuid: onuid, handler: nil}
			ioinfos = append(ioinfos, &oinfo)
		}
	}

	var handler *pcap.Handle
	nniup, nnidw := makeNniName(oltid)
	if handler, vethenv, err = setupVethHandler(nniup, nnidw, vethenv); err != nil {
		return ioinfos, vethenv, err
	}

	iinfo := Ioinfo{name: nnidw, iotype: "nni", ioloc: "inside", intfid: 1, handler: handler}
	ioinfos = append(ioinfos, &iinfo)
	oinfo := Ioinfo{name: nniup, iotype: "nni", ioloc: "outside", intfid: 1, handler: nil}
	ioinfos = append(ioinfos, &oinfo)
	return ioinfos, vethenv, nil
}

func (s *Server) runPacketInDaemon(stream openolt.Openolt_EnableIndicationServer) error {
	logger.Debug("runPacketInDaemon Start")
	unichannel := make(chan Packet, 2048)
	flag := false

	for intfid, _ := range s.Onumap {
		for _, onu := range s.Onumap[intfid] { //TODO: should be updated for multiple-Interface
			onuid := onu.OnuID
			ioinfo, err := s.identifyUniIoinfo("inside", intfid, onuid)
			if err != nil {
				logger.Error("Fail to identifyUniIoinfo (onuid: %d): %v\n", onuid, err)
				return err
			}
			uhandler := ioinfo.handler
			defer uhandler.Close()
			go RecvWorker(ioinfo, uhandler, unichannel)
		}
	}

	ioinfo, err := s.identifyNniIoinfo("inside")
	if err != nil {
		return err
	}
	nhandler := ioinfo.handler
	defer nhandler.Close()
	nnichannel := make(chan Packet, 32)
	go RecvWorker(ioinfo, nhandler, nnichannel)

	data := &openolt.Indication_PktInd{}
	for {
		select {
		case unipkt := <-unichannel:
			logger.Debug("Received packet in grpc Server from UNI.")
			if unipkt.Info == nil || unipkt.Info.iotype != "uni" {
				logger.Info("WARNING: This packet does not come from UNI ")
				continue
			}

			intfid := unipkt.Info.intfid
			onuid := unipkt.Info.onuid
			gemid, _ := getGemPortID(intfid, onuid)
			pkt := unipkt.Pkt
			layerEth := pkt.Layer(layers.LayerTypeEthernet)
			le, _ := layerEth.(*layers.Ethernet)
			ethtype := le.EthernetType

			if ethtype == 0x888e {
				logger.Debug("Received upstream packet is EAPOL.")
				//log.Println(unipkt.Pkt.Dump())
				//log.Println(pkt.Dump())
			} else if layerDHCP := pkt.Layer(layers.LayerTypeDHCPv4); layerDHCP != nil {
				logger.Debug("Received upstream packet is DHCP.")

				//C-TAG
				onu, _ := s.getOnuByID(onuid)
				sn := convB2S(onu.SerialNumber.VendorSpecific)
				if ctag, ok := s.CtagMap[sn]; ok == true {
					tagpkt, err := PushVLAN(pkt, uint16(ctag))
					if err != nil {
						logger.Error("Fail to tag C-tag")
					} else {
						pkt = tagpkt
					}
				} else {
					logger.Error("Could not find the onuid %d (SN: %s) in CtagMap %v!\n", onuid, sn, s.CtagMap)
				}
			} else {
				continue
			}

			logger.Debug("sendPktInd intfid:%d (onuid: %d) gemid:%d\n", intfid, onuid, gemid)
			data = &openolt.Indication_PktInd{PktInd: &openolt.PacketIndication{IntfType: "pon", IntfId: intfid, GemportId: gemid, Pkt: pkt.Data()}}
			if err := stream.Send(&openolt.Indication{Data: data}); err != nil {
				logger.Error("Fail to send PktInd indication. %v\n", err)
				return err
			}

		case nnipkt := <-nnichannel:
			if nnipkt.Info == nil || nnipkt.Info.iotype != "nni" {
				logger.Info("WARNING: This packet does not come from NNI ")
				continue
			}

			logger.Debug("Received packet in grpc Server from NNI.")
			intfid := nnipkt.Info.intfid
			pkt := nnipkt.Pkt
			logger.Info("sendPktInd intfid:%d\n", intfid)
			data = &openolt.Indication_PktInd{PktInd: &openolt.PacketIndication{IntfType: "nni", IntfId: intfid, Pkt: pkt.Data()}}
			if err := stream.Send(&openolt.Indication{Data: data}); err != nil {
				logger.Error("Fail to send PktInd indication. %v\n", err)
				return err
			}

		case <-s.Endchan:
			if flag == false {
				logger.Debug("PacketInDaemon thread receives close ")
				close(unichannel)
				logger.Debug("Closed unichannel ")
				close(nnichannel)
				logger.Debug("Closed nnichannel ")
				flag = true
				return nil
			}
		}
	}
	return nil
}

func (s *Server) exeAAATest() error {
	logger.Info("exeAAATest stands by....")
	infos, err := s.getUniIoinfos("outside")
	if err != nil {
		return err
	}

	univeths := []string{}
	for _, info := range infos {
		univeths = append(univeths, info.name)
	}

	for {
		select {
		case <-s.Endchan:
			logger.Debug("exeAAATest thread receives close ")
			return nil
		case <-time.After(time.Second * time.Duration(s.AAAWait)):
			err = setup.ActivateWPASups(univeths, s.Delay)
			if err != nil {
				return err
			}
			logger.Info("WPA Supplicants are successfully activated ")
			s.Processes = append(s.Processes, "wpa_supplicant")
			logger.Debug("Running Process:%v", s.Processes)
			return nil
		}
	}
	return nil
}

func (s *Server) exeDHCPTest() error {
	logger.Info("exeDHCPTest stands by....")
	info, err := s.identifyNniIoinfo("outside")

	if err != nil {
		return err
	}

	err = setup.ActivateDHCPServer(info.name, s.DhcpServerIP)
	if err != nil {
		return err
	}
	s.Processes = append(s.Processes, "dhcpd")
	logger.Debug("Running Process:%v", s.Processes)

	infos, err := s.getUniIoinfos("outside")
	if err != nil {
		return err
	}

	univeths := []string{}
	for _, info := range infos {
		univeths = append(univeths, info.name)
	}

	for {
		select {
		case <-s.Endchan:
			logger.Debug("exeDHCPTest thread receives close ")
			return nil
		case <-time.After(time.Second * time.Duration(s.DhcpWait)):
			err = setup.ActivateDHCPClients(univeths, s.Delay)
			if err != nil {
				return err
			}
			logger.Info("DHCP clients are successfully activated ")
			s.Processes = append(s.Processes, "dhclient")
			logger.Debug("Running Process:%v", s.Processes)
			return nil
		}
	}
	return nil
}

func (s *Server) onuPacketOut(intfid uint32, onuid uint32, rawpkt gopacket.Packet) error {
	layerEth := rawpkt.Layer(layers.LayerTypeEthernet)
	if layerEth != nil {
		pkt, _ := layerEth.(*layers.Ethernet)
		ethtype := pkt.EthernetType
		if ethtype == 0x888e {
			logger.Debug("Received downstream packet is EAPOL.")
			//log.Println(rawpkt.Dump())
		} else if layerDHCP := rawpkt.Layer(layers.LayerTypeDHCPv4); layerDHCP != nil {
			logger.Debug("Received downstream packet is DHCP.")
			//log.Println(rawpkt.Dump())
			rawpkt, _, _ = PopVLAN(rawpkt)
			rawpkt, _, _ = PopVLAN(rawpkt)
		} else {
			return nil
		}
		ioinfo, err := s.identifyUniIoinfo("inside", intfid, onuid)
		if err != nil {
			return err
		}
		handle := ioinfo.handler
		SendUni(handle, rawpkt)
		return nil
	}
	logger.Info("WARNING: Received packet is not supported")
	return nil
}

func (s *Server) uplinkPacketOut(rawpkt gopacket.Packet) error {
	poppkt, _, err := PopVLAN(rawpkt)
	poppkt, _, err = PopVLAN(poppkt)
	if err != nil {
		logger.Error("%s", err)
		return err
	}
	ioinfo, err := s.identifyNniIoinfo("inside")
	if err != nil {
		return err
	}
	handle := ioinfo.handler
	SendNni(handle, poppkt)
	return nil
}

func (s *Server) IsAllOnuActive(regonus map[uint32][]*device.Onu) bool {
	for _, onus := range regonus {
		for _, onu := range onus {
			if onu.GetIntStatus() != device.ONU_ACTIVATED {
				return false
			}
		}
	}
	return true
}

func getGemPortID(intfid uint32, onuid uint32) (uint32, error) {
	idx := uint32(0)
	return 1024 + (((MAX_ONUS_PER_PON*intfid + onuid - 1) * 7) + idx), nil
	//return uint32(1032 + 8 * (vid - 1)), nil
}

func (s *Server) getOnuBySN(sn *openolt.SerialNumber) (*device.Onu, error) {
	for _, onus := range s.Onumap {
		for _, onu := range onus {
			if device.ValidateSN(*sn, *onu.SerialNumber) {
				return onu, nil
			}
		}
	}
	err := errors.New("No mathced SN is found ")
	logger.Error("%s", err)
	return nil, err
}

func (s *Server) getOnuByID(onuid uint32) (*device.Onu, error) {
	for _, onus := range s.Onumap {
		for _, onu := range onus {
			if onu.OnuID == onuid {
				return onu, nil
			}
		}
	}
	err := errors.New("No matched OnuID is found ")
	logger.Error("%s", err)
	return nil, err
}

func makeUniName(oltid uint32, intfid uint32, onuid uint32) (upif string, dwif string) {
	upif = setup.UNI_VETH_UP_PFX + strconv.Itoa(int(oltid)) + "_" + strconv.Itoa(int(intfid)) + "_" + strconv.Itoa(int(onuid))
	dwif = setup.UNI_VETH_DW_PFX + strconv.Itoa(int(oltid)) + "_" + strconv.Itoa(int(intfid)) + "_" + strconv.Itoa(int(onuid))
	return
}

func makeNniName(oltid uint32) (upif string, dwif string) {
	upif = setup.NNI_VETH_UP_PFX + strconv.Itoa(int(oltid))
	dwif = setup.NNI_VETH_DW_PFX + strconv.Itoa(int(oltid))
	return
}

func cleanUpVeths(vethenv []string) error {
	if len(vethenv) > 0 {
		logger.Debug("cleanUpVeths called ")
		setup.TearVethDown(vethenv)
	}
	return nil
}

func killProcesses(pnames []string) error {
	for _, pname := range pnames {
		setup.KillProcess(pname)
	}
	return nil
}

func setupVethHandler(inveth string, outveth string, vethenv []string) (*pcap.Handle, []string, error) {
	logger.Debug("SetupVethHandler(%s, %s) called ", inveth, outveth)
	err1 := setup.CreateVethPairs(inveth, outveth)
	vethenv = append(vethenv, inveth)
	if err1 != nil {
		setup.RemoveVeths(vethenv)
		return nil, vethenv, err1
	}
	handler, err2 := getVethHandler(inveth)
	if err2 != nil {
		setup.RemoveVeths(vethenv)
		return nil, vethenv, err2
	}
	return handler, vethenv, nil
}

func getVethHandler(vethname string) (*pcap.Handle, error) {
	var (
		device       string = vethname
		snapshot_len int32  = 1518
		promiscuous  bool   = false
		err          error
		timeout      time.Duration = pcap.BlockForever
	)
	handle, err := pcap.OpenLive(device, snapshot_len, promiscuous, timeout)
	if err != nil {
		return nil, err
	}
	logger.Debug("Server handle is created for %s\n", vethname)
	return handle, nil
}

func convB2S(b []byte) string {
	s := ""
	for _, i := range b {
		s = s + strconv.FormatInt(int64(i/16), 16) + strconv.FormatInt(int64(i%16), 16)
	}
	return s
}
