/*
 * talkkonnect headless mumble client/gateway with lcd screen and channel control
 * Copyright (C) 2018-2019, Suvir Kumar <suvir@talkkonnect.com>
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/.
 *
 * Software distributed under the License is distributed on an "AS IS" basis,
 * WITHOUT WARRANTY OF ANY KIND, either express or implied. See the License
 * for the specific language governing rights and limitations under the
 * License.
 *
 * talkkonnect is the based on talkiepi and barnard by Daniel Chote and Tim Cooper
 *
 * The Initial Developer of the Original Code is
 * Suvir Kumar <suvir@talkkonnect.com>
 * Portions created by the Initial Developer are Copyright (C) Suvir Kumar. All Rights Reserved.
 *
 * Contributor(s):
 *
 * Suvir Kumar <suvir@talkkonnect.com>
 *
 * My Blog is at www.talkkonnect.com
 * The source code is hosted at github.com/talkkonnect
 *
 *
 */

package talkkonnect

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/allan-simon/go-singleinstance"
	"github.com/comail/colog"
	hd44780 "github.com/talkkonnect/go-hd44780"
	"github.com/talkkonnect/gumble/gumble"
	"github.com/talkkonnect/gumble/gumbleffmpeg"
	"github.com/talkkonnect/gumble/gumbleutil"
	_ "github.com/talkkonnect/gumble/opus"
	term "github.com/talkkonnect/termbox-go"
	"github.com/talkkonnect/volume-go"
)

var (
	currentChannelID     uint32
	prevChannelID        uint32
	prevParticipantCount int    = 0
	prevButtonPress      string = "none"
	maxchannelid         uint32
	tmessage             string
	isrepeattx           bool = true
	m                    string
)

type Talkkonnect struct {
	Config          *gumble.Config
	Client          *gumble.Client
	VoiceTarget     *gumble.VoiceTarget
	Name            string
	Address         string
	Username        string
	Ident           string
	TLSConfig       tls.Config
	ConnectAttempts uint
	Stream          *Stream
	ChannelName     string
	IsTransmitting  bool
	IsPlayStream    bool
	GPIOEnabled     bool
}

type ChannelsListStruct struct {
	chanID     uint32
	chanName   string
	chanParent *gumble.Channel
	chanUsers  int
}

func Init(file string, ServerIndex string) {
	err := term.Init()
	if err != nil {
		FatalCleanUp("Cannot Initialize Terminal Error: " + err.Error())
	}
	defer term.Close()

	colog.Register()
	colog.SetOutput(os.Stdout)

	ConfigXMLFile = file
	err = readxmlconfig(ConfigXMLFile, false)
	if err != nil {
		message := err.Error()
		FatalCleanUp(message)
	}

	if Config.Global.Software.Settings.SingleInstance {
		lockFile, err := singleinstance.CreateLockFile("talkkonnect.lock")
		if err != nil {
			log.Println("error: Another Instance of talkkonnect is already running!!, Killing this Instance")
			time.Sleep(5 * time.Second)
			TTSEvent("quittalkkonnect")
			CleanUp()
		}
		defer lockFile.Close()
	}

	if Config.Global.Software.Settings.Logging == "screen" {
		colog.SetFlags(log.Ldate | log.Ltime)
	}

	if Config.Global.Software.Settings.Logging == "screenwithlineno" || Config.Global.Software.Settings.Logging == "screenandfilewithlineno" {
		colog.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	}

	switch Config.Global.Software.Settings.Loglevel {
	case "trace":
		colog.SetMinLevel(colog.LTrace)
		log.Println("info: Loglevel Set to Trace")
	case "debug":
		colog.SetMinLevel(colog.LDebug)
		log.Println("info: Loglevel Set to Debug")
	case "info":
		colog.SetMinLevel(colog.LInfo)
		log.Println("info: Loglevel Set to Info")
	case "warning":
		colog.SetMinLevel(colog.LWarning)
		log.Println("info: Loglevel Set to Warning")
	case "error":
		colog.SetMinLevel(colog.LError)
		log.Println("info: Loglevel Set to Error")
	case "alert":
		colog.SetMinLevel(colog.LAlert)
		log.Println("info: Loglevel Set to Alert")
	default:
		colog.SetMinLevel(colog.LInfo)
		log.Println("info: Default Loglevel unset in XML config automatically loglevel to Info")
	}

	if Config.Global.Software.AutoProvisioning.Enabled {
		log.Println("info: Contacting http Provisioning Server Pls Wait")
		err := autoProvision()
		time.Sleep(5 * time.Second)
		if err != nil {
			FatalCleanUp("Error from AutoProvisioning Module " + err.Error())
		} else {
			log.Println("info: Loading XML Config")
			ConfigXMLFile = file
			readxmlconfig(ConfigXMLFile, false)
		}
	}

	if Config.Global.Software.Settings.NextServerIndex > 0 {
		AccountIndex = Config.Global.Software.Settings.NextServerIndex
	} else {
		AccountIndex, _ = strconv.Atoi(ServerIndex)
	}

	b := Talkkonnect{
		Config:      gumble.NewConfig(),
		Name:        Name[AccountIndex],
		Address:     Server[AccountIndex],
		Username:    Username[AccountIndex],
		Ident:       Ident[AccountIndex],
		ChannelName: Channel[AccountIndex],
	}

	if Config.Global.Software.RemoteControl.MQTT.Enabled {
		log.Printf("info: Attempting to Contact MQTT Server")
		log.Printf("info: MQTT Broker      : %s\n", Config.Global.Software.RemoteControl.MQTT.Settings.MQTTBroker)
		log.Printf("info: Subscribed topic : %s\n", Config.Global.Software.RemoteControl.MQTT.Settings.MQTTTopic)
		go b.mqttsubscribe()
	} else {
		log.Printf("info: MQTT Server Subscription Disabled in Config")
	}

	MACName := ""
	if len(b.Username) == 0 {
		macaddress, err := getMacAddr()
		if err != nil {
			log.Println("error: Could Not Get Network Interface MAC Address")
		} else {
			for _, a := range macaddress {
				tmacname := a
				MACName = strings.Replace(tmacname, ":", "", -1)
			}
		}
		if len(MACName) == 0 {
			buf := make([]byte, 6)
			_, err := rand.Read(buf)
			if err != nil {
				FatalCleanUp("Cannot Generate Random Number Error " + err.Error())
			}
			buf[0] |= 2
			b.Config.Username = fmt.Sprintf("talkkonnect-%02x%02x%02x%02x%02x%02x", buf[0], buf[1], buf[2], buf[3], buf[4], buf[5])
		} else {
			b.Config.Username = fmt.Sprintf("talkkonnect-%v", MACName)
		}
	} else {
		b.Config.Username = Username[AccountIndex]
	}

	log.Printf("info: Connecting to Server %v Identified As %v With Username %v\n", Config.Accounts.Account[AccountIndex].ServerAndPort, Config.Accounts.Account[AccountIndex].Name, b.Config.Username)
	b.Config.Password = Password[AccountIndex]

	if Insecure[AccountIndex] {
		b.TLSConfig.InsecureSkipVerify = true
	}
	if Certificate[AccountIndex] != "" {
		cert, err := tls.LoadX509KeyPair(Certificate[AccountIndex], Certificate[AccountIndex])
		if err != nil {
			FatalCleanUp("Certificate Error " + err.Error())
		}
		b.TLSConfig.Certificates = append(b.TLSConfig.Certificates, cert)
	}

	if Config.Global.Software.RemoteControl.HTTP.Enabled && !HTTPServRunning {
		go func() {
			http.HandleFunc("/", b.httpAPI)
			if err := http.ListenAndServe(":"+Config.Global.Software.RemoteControl.HTTP.ListenPort, nil); err != nil {
				FatalCleanUp("Problem Starting HTTP API Server " + err.Error())
			}
		}()
	}

	b.ClientStart()

	IsConnected = false

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	exitStatus := 0

	<-sigs
	CleanUp()
	os.Exit(exitStatus)
}

func (b *Talkkonnect) ClientStart() {
	f, err := os.OpenFile(Config.Global.Software.Settings.LogFilenameAndPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	log.Println("info: Trying to Open File ", Config.Global.Software.Settings.LogFilenameAndPath)
	if err != nil {
		FatalCleanUp("Problem Opening talkkonnect.log file " + err.Error())
	}

	if Config.Global.Hardware.TargetBoard == "rpi" {
		if !Config.Global.Hardware.LedStripEnabled {
			GPIOOutAll("led/relay", "off")
		}
	}

	if Config.Global.Software.Settings.Logging == "screenandfile" {
		log.Println("info: Logging is set to: ", Config.Global.Software.Settings.Logging)
		wrt := io.MultiWriter(os.Stdout, f)
		colog.SetFlags(log.Ldate | log.Ltime)
		colog.SetOutput(wrt)
	}

	if Config.Global.Software.Settings.Logging == "screenandfilewithlineno" {
		log.Println("info: Logging is set to: ", Config.Global.Software.Settings.Logging)
		wrt := io.MultiWriter(os.Stdout, f)
		colog.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
		colog.SetOutput(wrt)
	}

	b.Config.Attach(gumbleutil.AutoBitrate)
	b.Config.Attach(b)

	log.Printf("info: [%d] Default Mumble Accounts Found in XML config\n", AccountCount)

	if Config.Global.Hardware.TargetBoard == "rpi" {
		log.Println("info: Target Board Set as RPI (gpio enabled) ")
		b.initGPIO()
		if Config.Global.Hardware.LedStripEnabled {
			MyLedStrip, _ = NewLedStrip()
			log.Printf("info: Led Strip %v %s\n", MyLedStrip.buf, MyLedStrip.display)
		}
	} else {
		log.Println("info: Target Board Set as PC (gpio disabled) ")
	}

	if (Config.Global.Hardware.TargetBoard == "rpi" && Config.Global.Hardware.LCD.BacklightTimerEnabled) && (OLEDEnabled || Config.Global.Hardware.LCD.Enabled) {

		log.Println("info: Backlight Timer Enabled by Config")
		BackLightTime = *BackLightTimePtr
		BackLightTime = time.NewTicker(time.Duration(Config.Global.Hardware.LCD.BackLightTimeoutSecs) * time.Second)

		go func() {
			for {
				<-BackLightTime.C
				log.Printf("debug: LCD Backlight Ticker Timed Out After %d Seconds", LCDBackLightTimeout)
				LCDIsDark = true
				if LCDInterfaceType == "parallel" {
					GPIOOutPin("backlight", "off")
				}
				if LCDInterfaceType == "i2c" {
					lcd := hd44780.NewI2C4bit(LCDI2CAddress)
					if err := lcd.Open(); err != nil {
						log.Println("error: Can't open lcd: " + err.Error())
						return
					}
					lcd.ToggleBacklight()
				}
				if OLEDEnabled && OLEDInterfacetype == "i2c" {
					Oled.DisplayOff()
					LCDIsDark = true
				}
			}
		}()
	} else {
		log.Println("debug: Backlight Timer Disabled by Config")
	}

	talkkonnectBanner("\u001b[44;1m") // add blue background to banner reference https://www.lihaoyi.com/post/BuildyourownCommandLinewithANSIescapecodes.html#background-colors

	err = volume.Unmute(Config.Global.Software.Settings.OutputDevice)

	if err != nil {
		log.Println("error: Unable to Unmute ", err)
	} else {
		log.Println("debug: Speaker UnMuted Before Connect to Server")
	}

	TTSEvent("talkkonnectloaded")

	b.Connect()

	pstream = gumbleffmpeg.New(b.Client, gumbleffmpeg.SourceFile(""), 0)

	if (Config.Global.Hardware.HeartBeat.Enabled) && (Config.Global.Hardware.TargetBoard == "rpi") {
		HeartBeat := time.NewTicker(time.Duration(Config.Global.Hardware.HeartBeat.Periodmsecs) * time.Millisecond)

		go func() {
			for range HeartBeat.C {
				timer1 := time.NewTimer(time.Duration(Config.Global.Hardware.HeartBeat.LEDOnmsecs) * time.Millisecond)
				timer2 := time.NewTimer(time.Duration(Config.Global.Hardware.HeartBeat.LEDOffmsecs) * time.Millisecond)
				<-timer1.C
				if Config.Global.Hardware.HeartBeat.Enabled {
					GPIOOutPin("heartbeat", "on")
				}
				<-timer2.C
				if Config.Global.Hardware.HeartBeat.Enabled {
					GPIOOutPin("heartbeat", "off")
				}
				if KillHeartBeat {
					HeartBeat.Stop()
				}

			}
		}()
	}

	if Config.Global.Software.Beacon.Enabled {
		BeaconTicker := time.NewTicker(time.Duration(Config.Global.Software.Beacon.BeaconTimerSecs) * time.Second)

		go func() {
			for range BeaconTicker.C {
				IsPlayStream = true
				b.playIntoStream(Config.Global.Software.Beacon.BeaconFileAndPath, Config.Global.Software.Beacon.Volume)
				IsPlayStream = false
				log.Println("info: Beacon Enabled and Timed Out Auto Played File ", Config.Global.Software.Beacon.BeaconFileAndPath, " Into Stream")
			}
		}()
	}

	b.BackLightTimer()

	if LCDEnabled {
		GPIOOutPin("backlight", "on")
		LCDIsDark = false
	}

	if OLEDEnabled {
		Oled.DisplayOn()
		LCDIsDark = false
	}

	if Config.Global.Hardware.AudioRecordFunction.Enabled {

		if Config.Global.Hardware.AudioRecordFunction.RecordOnStart {

			if Config.Global.Hardware.AudioRecordFunction.RecordMode != "" {

				if Config.Global.Hardware.AudioRecordFunction.RecordMode == "traffic" {
					log.Println("info: Incoming Traffic will be Recorded with sox")
					AudioRecordTraffic()
					if Config.Global.Hardware.TargetBoard == "rpi" {
						if LCDEnabled {
							LcdText = [4]string{"nil", "nil", "nil", "Traffic Recording ->"} // 4
							LcdDisplay(LcdText, LCDRSPin, LCDEPin, LCDD4Pin, LCDD5Pin, LCDD6Pin, LCDD7Pin, LCDInterfaceType, LCDI2CAddress)
						}
						if OLEDEnabled {
							oledDisplay(false, 6, 1, "Traffic Recording") // 6
						}
					}
				}
				if Config.Global.Hardware.AudioRecordFunction.RecordMode == "ambient" {
					log.Println("info: Ambient Audio from Mic will be Recorded with sox")
					AudioRecordAmbient()
					if Config.Global.Hardware.TargetBoard == "rpi" {
						if LCDEnabled {
							LcdText = [4]string{"nil", "nil", "nil", "Mic Recording ->"} // 4
							LcdDisplay(LcdText, LCDRSPin, LCDEPin, LCDD4Pin, LCDD5Pin, LCDD6Pin, LCDD7Pin, LCDInterfaceType, LCDI2CAddress)
						}
						if OLEDEnabled {
							oledDisplay(false, 6, 1, "Mic Recording") // 6
						}
					}
				}
				if Config.Global.Hardware.AudioRecordFunction.RecordMode == "combo" {
					log.Println("info: Both Incoming Traffic and Ambient Audio from Mic will be Recorded with sox")
					AudioRecordCombo()
					if Config.Global.Hardware.TargetBoard == "rpi" {
						if LCDEnabled {
							LcdText = [4]string{"nil", "nil", "nil", "Combo Recording ->"} // 4
							LcdDisplay(LcdText, LCDRSPin, LCDEPin, LCDD4Pin, LCDD5Pin, LCDD6Pin, LCDD7Pin, LCDInterfaceType, LCDI2CAddress)
						}
						if OLEDEnabled {
							oledDisplay(false, 6, 1, "Combo Recording") //6
						}
					}
				}

			}

		}
	}

	if Config.Global.Hardware.USBKeyboard.Enabled && len(Config.Global.Hardware.USBKeyboard.USBKeyboardPath) > 0 {
		go b.USBKeyboard()
	}

	if Register[AccountIndex] && !b.Client.Self.IsRegistered() {
		b.Client.Self.Register()
		log.Println("alert: Client Is Now Registered")
	} else {
		log.Println("info: Client Is Already Registered")

	}

	if Config.Global.Software.Settings.StreamOnStart {
		time.Sleep(Config.Global.Software.Settings.StreamOnStartAfter * time.Second)
		b.cmdPlayback()
	}

	if Config.Global.Software.Settings.TXOnStart {
		time.Sleep(Config.Global.Software.Settings.TXOnStartAfter * time.Second)
		b.cmdStartTransmitting()
	}

	go func() {
		for {
			select {
			case <-Talking:
				v := <-Talking
				TalkedTicker.Reset(200 * time.Millisecond)
				if LastSpeaker != v.WhoTalking {
					LastSpeaker = v.WhoTalking
					t := time.Now()
					if Config.Global.Hardware.TargetBoard == "rpi" {
						if LCDEnabled {
							GPIOOutPin("backlight", "on")
							lcdtext = [4]string{"nil", "", "", LastSpeaker + " " + t.Format("15:04:05")}
							LcdDisplay(lcdtext, LCDRSPin, LCDEPin, LCDD4Pin, LCDD5Pin, LCDD6Pin, LCDD7Pin, LCDInterfaceType, LCDI2CAddress)
							BackLightTime.Reset(time.Duration(LCDBackLightTimeout) * time.Second)
						}

						if OLEDEnabled {
							Oled.DisplayOn()
							go oledDisplay(false, 3, 1, LastSpeaker+" "+t.Format("15:04:05"))
							BackLightTime.Reset(time.Duration(LCDBackLightTimeout) * time.Second)
						}
					}
				}

				if !RXLEDStatus {
					RXLEDStatus = true
					log.Println("info: Speaking->", v.WhoTalking)
					if !Config.Global.Hardware.LedStripEnabled {
						GPIOOutPin("voiceactivity", "on")
					} else {
						MyLedStripOnlineLEDOn()
					}
				}
			case <-TalkedTicker.C:
				RXLEDStatus = false
				if !Config.Global.Hardware.LedStripEnabled {
					GPIOOutPin("voiceactivity", "off")
				} else {
					MyLedStripOnlineLEDOff()
				}
				TalkedTicker.Stop()
			}
		}
	}()

keyPressListenerLoop:
	for {
		switch ev := term.PollEvent(); ev.Type {
		case term.EventKey:
			switch ev.Key {
			case term.KeyEsc:
				log.Println("error: ESC Key is Invalid")
				reset()
				break keyPressListenerLoop
			case term.KeyDelete:
				b.cmdDisplayMenu()
			case term.KeyF1:
				b.cmdChannelUp()
			case term.KeyF2:
				b.cmdChannelDown()
			case term.KeyF3:
				b.cmdMuteUnmute("toggle")
			case term.KeyF4:
				b.cmdCurrentVolume()
			case term.KeyF5:
				b.cmdVolumeUp()
			case term.KeyF6:
				b.cmdVolumeDown()
			case term.KeyF7:
				b.cmdListServerChannels()
			case term.KeyF8:
				b.cmdStartTransmitting()
			case term.KeyF9:
				b.cmdStopTransmitting()
			case term.KeyF10:
				b.cmdListOnlineUsers()
			case term.KeyF11:
				b.cmdPlayback()
			case term.KeyF12:
				b.cmdGPSPosition()
			case term.KeyCtrlB:
				b.cmdLiveReload()
			case term.KeyCtrlC:
				talkkonnectAcknowledgements("\u001b[44;1m") // add blue background to banner reference https://www.lihaoyi.com/post/BuildyourownCommandLinewithANSIescapecodes.html#background-colors
				b.cmdQuitTalkkonnect()
			case term.KeyCtrlD:
				b.cmdDebugStacktrace()
			case term.KeyCtrlE:
				b.cmdSendEmail()
			case term.KeyCtrlF:
				b.cmdConnPreviousServer()
			case term.KeyCtrlH:
				cmdSanityCheck()
			case term.KeyCtrlI: // New. Audio Recording. Traffic
				b.cmdAudioTrafficRecord()
			case term.KeyCtrlJ: // New. Audio Recording. Mic
				b.cmdAudioMicRecord()
			case term.KeyCtrlK: // New/ Audio Recording. Combo
				b.cmdAudioMicTrafficRecord()
			case term.KeyCtrlL:
				b.cmdClearScreen()
			case term.KeyCtrlO:
				b.cmdPingServers()
			case term.KeyCtrlN:
				b.cmdConnNextServer()
			case term.KeyCtrlP:
				b.cmdPanicSimulation()
			case term.KeyCtrlG:
				b.cmdPlayRepeaterTone()
			case term.KeyCtrlR:
				b.cmdRepeatTxLoop()
			case term.KeyCtrlS:
				b.cmdScanChannels()
			case term.KeyCtrlT:
				cmdThanks()
			case term.KeyCtrlU:
				b.cmdShowUptime()
			case term.KeyCtrlV:
				b.cmdDisplayVersion()
			case term.KeyCtrlX:
				b.cmdDumpXMLConfig()
			default:
				if _, ok := TTYKeyMap[ev.Ch]; ok {
					switch strings.ToLower(TTYKeyMap[ev.Ch].Command) {
					case "channelup":
						b.cmdChannelUp()
					case "channeldown":
						b.cmdChannelDown()
					case "serverup":
						b.cmdConnNextServer()
					case "serverdown":
						b.cmdConnPreviousServer()
					case "mute":
						b.cmdMuteUnmute("mute")
					case "unmute":
						b.cmdMuteUnmute("unmute")
					case "mute-toggle":
						b.cmdMuteUnmute("toggle")
					case "stream-toggle":
						b.cmdPlayback()
					case "volumeup":
						b.cmdVolumeUp()
					case "volumedown":
						b.cmdVolumeDown()
					case "setcomment":
						if TTYKeyMap[ev.Ch].ParamValue == "setcomment" {
							log.Println("info: Set Commment ", TTYKeyMap[ev.Ch].ParamValue)
							b.Client.Self.SetComment(TTYKeyMap[ev.Ch].ParamValue)
						}
					case "transmitstart":
						b.cmdStartTransmitting()
					case "transmitstop":
						b.cmdStopTransmitting()
					case "record":
						b.cmdAudioTrafficRecord()
						b.cmdAudioMicRecord()
					case "voicetargetset":
						Paramvalue, err := strconv.Atoi(TTYKeyMap[ev.Ch].ParamValue)
						if err != nil {
							log.Printf("error: Error Message %v, %v Is Not A Number", err, Paramvalue)
						}
						b.cmdSendVoiceTargets(uint32(Paramvalue))
					default:
						log.Println("Command Not Defined ", strings.ToLower(TTYKeyMap[ev.Ch].Command))
					}
				} else {
					log.Println("error: Key Not Mapped ASC ", ev.Ch)
				}
			}
		case term.EventError:
			FatalCleanUp("Terminal Error " + err.Error())
		}
	}
}
