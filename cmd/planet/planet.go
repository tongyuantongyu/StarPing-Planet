// StarPing Planet
// Copyright (C) 2020  Yuan Tong
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"starping/tools"
	"strings"
	"time"
)

//import _ "net/http/pprof"

var (
	_secret = flag.String("k", "secret", "Authorization Key")
	name    = flag.String("n", "planet", "Name of this planet")
	server  = flag.String("s", "127.0.0.1:8080", "Star to send report to")
	https   = flag.Bool("t", false, "Use HTTPS to connect the server")
	retry   = flag.String("r", "60,64;3600,64", "Retry pattern."+
		" Semicolon(;) splits retries with format time(second),capacity. specially 0 means no retry")
	logFile       = flag.String("l", "", "Log file.")
	level         = flag.Int("v", 2, "Verbose level.")
	timeout       = flag.Int("w", 1000, "Report send timeout(ms)")
	refresh       = flag.Int("f", 3600, "Config update interval(ms)")
	license       = flag.Bool("license", false, "Show license.")
	reportLink    string
	configLink    string
	configULink   string
	secret        []byte
	reportChannel chan *ReportContainer
	failedChannel chan *ReportContainer
	fileLogger    *log.Logger
	congestWarn   = false
)

const (
	ERROR = iota
	WARNING
	INFO
	DEBUG
)

type Report struct {
	Time   int64       `json:"time"`
	Report interface{} `json:"report"`
}

type ReportContainer struct {
	Type      string
	Signature string
	Target    string
	Report    *[]byte
}

type Config struct {
	PingConf    *tools.PingConfig `json:"ping_config"`
	MTRConf     *tools.MTRConfig  `json:"mtr_config"`
	PingTargets *[]string         `json:"ping_targets"`
	MTRTargets  *[]string         `json:"mtr_targets"`
}

type ErrResponse struct {
	Msg string `json:"message"`
}

func init() {
	flag.Usage = func() {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "StarPing Planet node. Copyright (C) 2020  Yuan Tong\nUsage: ")
		flag.PrintDefaults()
		_, _ = fmt.Fprintln(flag.CommandLine.Output(),
			"\nThis program comes with ABSOLUTELY NO WARRANTY;\n"+
				"This is free software, and you are welcome to redistribute it\n"+
				"under certain conditions. Use -license flag for details.")
	}

	flag.Parse()

	if *license {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(),
			"StarPing Planet\n"+
				"Copyright (C) 2020  Yuan Tong\n\n"+
				"This program is free software: you can redistribute it and/or modify\n"+
				"it under the terms of the GNU General Public License as published by\n"+
				"the Free Software Foundation, either version 3 of the License, or\n"+
				"(at your option) any later version.\n\n"+
				"This program is distributed in the hope that it will be useful,\n"+
				"but WITHOUT ANY WARRANTY; without even the implied warranty of\n"+
				"MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the\n"+
				"GNU General Public License for more details.\n\n"+
				"You should have received a copy of the GNU General Public License\n"+
				"along with this program.  If not, see <https://www.gnu.org/licenses/>.")
		os.Exit(0)
	}

	secret = []byte(*_secret)

	scheme := "http"
	if *https {
		scheme = "https"
	}
	reportLink = fmt.Sprintf("%s://%s/report?type=%%s", scheme, *server)
	configLink = fmt.Sprintf("%s://%s/config?nocache=1", scheme, *server)
	configULink = fmt.Sprintf("%s://%s/config?update=1&nocache=1", scheme, *server)

	reportChannel = make(chan *ReportContainer)
	failedChannel = make(chan *ReportContainer)
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			log.Printf("[Warning] Can't open log file '%s': %s.\n", *logFile, err)
		} else {
			fileLogger = log.New(f, "", log.LstdFlags)
		}
	}

	if *level >= WARNING {
		go func() {
			ticker := time.NewTicker(2 * time.Minute)
			for {
				<-ticker.C
				congestWarn = false
			}
		}()
	}
	//go func() {
	//    log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
	//}()
}

func logE(info string, v ...interface{}) {
	if *level < ERROR {
		os.Exit(1)
	}
	log.Printf("[Error] "+info, v...)
	if fileLogger != nil {
		fileLogger.Fatalf("[Error] "+info, v...)
	} else {
		os.Exit(1)
	}
}

func logW(info string, v ...interface{}) {
	if *level < WARNING {
		return
	}
	log.Printf("[Warning] "+info, v...)
	if fileLogger != nil {
		fileLogger.Printf("[Warning] "+info, v...)
	}
}

func logI(info string, v ...interface{}) {
	if *level < INFO {
		return
	}
	log.Printf("[Info] "+info, v...)
	if fileLogger != nil {
		fileLogger.Printf("[Info] "+info, v...)
	}
}

func logD(info string, v ...interface{}) {
	if *level < DEBUG {
		return
	}
	log.Printf("[Debug] "+info, v...)
	if fileLogger != nil {
		fileLogger.Printf("[Debug] "+info, v...)
	}
}

func warnCongested() {
	if !congestWarn {
		logW("A level of retry sender is congested. Such situation may caused by " +
			"the star or the network of Planet down and lots of report retry pending. " +
			"You should consider increasing your retry buffer size or decrease request wait time.\n")
	}
}

func main() {
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			MaxConnsPerHost:     100,
		},
	}
	client.Timeout = time.Duration(*timeout) * time.Millisecond
	//defer func() {
	//    if p := recover(); p != nil {
	//        network.FinishICMPManager()
	//        client.CloseIdleConnections()
	//        logE("Planet panicked: %s", p)
	//    }
	//}()

	// report retry flow
	if *retry == "0" {
		proc := make(chan *ReportContainer)
		wait := make(chan *ReportContainer)
		go deliveryFailed(proc, wait)
		go DrainTrash(proc, wait)
	} else {
		r := strings.Split(*retry, ";")
		rc := make([]struct {
			Wait     int
			Capacity int
		}, len(r))
		for i, conf := range r {
			_, err := fmt.Sscanf(conf, "%d,%d", &rc[i].Wait, &rc[i].Capacity)
			if err != nil {
				logE("Bad retry config", err)
			}
		}
		procN := make(chan *ReportContainer, rc[0].Capacity)
		waitN := make(chan *ReportContainer, rc[0].Capacity)
		go deliveryFailed(procN, waitN)
		proc, wait := procN, waitN
		for i := 0; i < len(r)-1; i++ {
			procN = make(chan *ReportContainer, rc[i+1].Capacity)
			waitN = make(chan *ReportContainer, rc[i+1].Capacity)
			go flipFlopReporter(client, proc, wait, procN, waitN, time.Duration(rc[i].Wait)*time.Second)
			proc, wait = procN, waitN
		}
		procN = make(chan *ReportContainer)
		waitN = make(chan *ReportContainer)
		go flipFlopReporter(client, proc, wait, procN, waitN, time.Duration(rc[len(r)-1].Wait)*time.Second)
		go DrainTrash(procN, waitN)
	}

	// report goroutine
	go func() {
		for {
			report := <-reportChannel
			go sender(client, report)
		}
	}()

	// start work goroutine
	config := getConfig(client)
	pingInterval := time.Duration(int64(config.PingConf.Frequency) / int64(len(*config.PingTargets)))
	traceInterval := time.Duration(int64(config.MTRConf.Frequency) / int64(len(*config.MTRTargets)))

	logI("Aligning ping time.")
	startTime := time.Unix(0, (time.Now().UnixNano()/int64(config.PingConf.
		Frequency)+1)*int64(config.PingConf.Frequency))
	if startTime.Before(time.Now()) {
		startTime = startTime.Add(config.PingConf.Frequency)
	}
	time.Sleep(time.Until(startTime))
	go runPeriodical(func() {
		logI("Start probing latency data of %d targets.\n", len(*config.PingTargets))
		ticker := time.NewTicker(pingInterval)
		pingTargets := make([]string, len(*config.PingTargets))
		copy(pingTargets, *config.PingTargets)
		for _, addr := range pingTargets {
			go pingRoutine(addr, config.PingConf)
			<-ticker.C
		}
		ticker.Stop()
	}, config.PingConf.Frequency)
	go runPeriodical(func() {
		logI("Start probing route data of %d targets.\n", len(*config.MTRTargets))
		ticker := time.NewTicker(traceInterval)
		mtrTargets := make([]string, len(*config.MTRTargets))
		copy(mtrTargets, *config.MTRTargets)
		for _, addr := range mtrTargets {
			go mtrRoutine(addr, config.MTRConf)
			<-ticker.C
		}
		ticker.Stop()
	}, config.MTRConf.Frequency)

	// update config periodically
	time.Sleep(time.Duration(*refresh) * time.Second)
	go runPeriodical(func() {
		updateConfig(client, config)
	}, time.Duration(*refresh)*time.Second)

	// block main goroutine
	block := make(chan struct{})
	<-block
}

func getConfig(client *http.Client) *Config {
	request, _ := http.NewRequest("GET", configLink, nil)
	request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(*name))
	request.Header.Set("X-StarPing-Name", *name)
	request.Header.Set("X-StarPing-Signature", fmt.Sprintf("%x", h.Sum(nil)))
	resp, err := client.Do(request)
	if err != nil {
		logE("Can't get config from Star: %s\n", err)
	}
	configByte, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logE("Can't get config from Star: Failed reading response body: \n", err)
	}
	if resp.StatusCode != http.StatusOK {
		errSrv := &ErrResponse{}
		err = json.Unmarshal(bytes.Trim(configByte, "\x00"), errSrv)
		if err != nil {
			logE("Can't get config from Star: Server error: %s\n", string(bytes.Trim(configByte, "\x00")))
		} else {
			logE("Can't get config from Star: Server error: %s\n", errSrv.Msg)
		}
	}
	config := &Config{}
	err = json.Unmarshal(bytes.Trim(configByte, "\x00"), config)
	if err != nil {
		logE("Can't get config from Star: Bad Config response: %s\n", string(bytes.Trim(configByte, "\x00")))
	}
	logI("Got config from server.\n")
	return config
}

func updateConfig(client *http.Client, config *Config) *Config {
	request, _ := http.NewRequest("GET", configULink, nil)
	request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(*name))
	request.Header.Set("X-StarPing-Name", *name)
	request.Header.Set("X-StarPing-Signature", fmt.Sprintf("%x", h.Sum(nil)))
	resp, err := client.Do(request)
	if err != nil {
		logW("Can't update config from Star: %s\n", err)
		return config
	}
	configByte, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logW("Can't update config from Star: Failed reading response body: \n", err)
		return config
	}
	if resp.StatusCode != http.StatusOK {
		errSrv := &ErrResponse{}
		err = json.Unmarshal(bytes.Trim(configByte, "\x00"), errSrv)
		if err != nil {
			logW("Can't update config from Star: Server error: %s\n", string(bytes.Trim(configByte, "\x00")))
		} else {
			logW("Can't update config from Star: Server error: %s\n", errSrv.Msg)
		}
		return config
	}
	_test := &Config{}
	err = json.Unmarshal(bytes.Trim(configByte, "\x00"), _test)
	if err != nil {
		logW("Can't update config from Star: Bad Config response: %s\n", string(bytes.Trim(configByte, "\x00")))
		return config
	}
	_ = json.Unmarshal(bytes.Trim(configByte, "\x00"), config)
	logI("Config updated from server.\n")
	return config
}

func runPeriodical(function func(), freq time.Duration) {
	ticker := time.NewTicker(freq)
	for {
		go function()
		<-ticker.C
	}
}

func pingRoutine(addr string, config *tools.PingConfig) {
	logD("Ping IP: %s\n", addr)
	t := time.Now().UnixNano()
	result, err := tools.Ping(addr, config)
	if err == nil {
		j, err := json.Marshal(Report{
			Time:   t,
			Report: result,
		})
		if err != nil {
			logW("Failed marshalling Ping report for IP %s: %s", addr, err)
		}
		report := ReportContainer{
			Type:   "ping",
			Target: addr,
			Report: &j,
		}
		report.Sign()
		reportChannel <- &report
	}
}

func mtrRoutine(addr string, config *tools.MTRConfig) {
	logD("MTR IP: %s\n", addr)
	t := time.Now().UnixNano()
	result, err := tools.MTR(addr, config)
	if err == nil {
		j, err := json.Marshal(Report{
			Time:   t,
			Report: result,
		})
		if err != nil {
			logW("Failed marshalling MTR report for IP %s: %s", addr, err)
		}
		report := ReportContainer{
			Type:   "mtr",
			Target: addr,
			Report: &j,
		}
		report.Sign()
		reportChannel <- &report
	}
}

func (report *ReportContainer) Sign() {
	h := hmac.New(sha256.New, secret)
	h.Write(*report.Report)
	report.Signature = fmt.Sprintf("%x", h.Sum(nil))
}

func requestBuilder(report *ReportContainer) (request *http.Request) {
	request, _ = http.NewRequest("POST", fmt.Sprintf(reportLink, report.Type), bytes.NewReader(*report.Report))
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.Header.Set("X-StarPing-Name", *name)
	request.Header.Set("X-StarPing-Signature", report.Signature)
	return
}

func flipFlopReporter(client *http.Client, proc, wait, main, full chan *ReportContainer, interval time.Duration) {
	timer := time.NewTimer(interval)
	for {
		var semaphore *ReportContainer = nil
		select {
		// when proc is full, report will be sent into wait and trigger force report
		case semaphore = <-wait:
			logI("Retry reporter with interval %s triggered by queue full.\n", interval)
			timer.Stop()
			// drain the proc channel
			for i := 0; i < len(proc); i++ {
				report := <-proc
				main, full = flipFlopSender(client, report, main, full)
			}
			// send the semaphore
			main, full = flipFlopSender(client, semaphore, main, full)
			semaphore = nil
			// now wait before will be used to receive report to be proc
			// and proc before will be used to wait for semaphore
			// so called flip-flop
			proc, wait = wait, proc
		// or when timer fired then
		case <-timer.C:
			if len(proc) != 0 {
				logI("Retry reporter with interval %s triggered by timer fired.\n", interval)
				// drain the proc channel
				for i := 0; i < len(proc); i++ {
					report := <-proc
					main, full = flipFlopSender(client, report, main, full)
				}
			}
		}
		// reset timer status
		select {
		case <-timer.C:
		default:
		}
		timer.Reset(interval)
	}
}

func sender(client *http.Client, report *ReportContainer) {
	logD("Sending %s report of %s\n", report.Type, report.Target)
	resp, err := client.Do(requestBuilder(report))
	if netErr, ok := err.(net.Error); ok {
		logI("Failed sending %s report of %s, network error: %s. issue resend.\n", report.Type, report.Target, netErr)
		failedChannel <- report
	} else if err != nil {
		logW("Failed sending %s report of %s, unrecoverable error: %s Discard.\n", report.Type, report.Target, err)
	} else {
		if resp.StatusCode != 200 {
			errByte, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				logW("Failed sending %s report of %s, HTTP Status %d, failed reading response body: \n", report.Type,
					report.Target, resp.StatusCode, err)
				return
			}
			errSrv := &ErrResponse{}
			err = json.Unmarshal(bytes.Trim(errByte, "\x00"), errSrv)
			if err != nil {
				logW("Failed sending %s report of %s, HTTP Status %d: %s\n", report.Type,
					report.Target, resp.StatusCode, string(bytes.Trim(errByte, "\x00")))
			} else {
				logW("Failed sending %s report of %s, HTTP Status %d: %s\n", report.Type,
					report.Target, resp.StatusCode, errSrv.Msg)
			}
		} else {
			// Drain the Body to enable Keep-Alive
			_, _ = io.Copy(ioutil.Discard, resp.Body)
		}
	}
}

func deliveryFailed(main, full chan *ReportContainer) {
	for {
		report := <-failedChannel
		select {
		// send failed report to main channel if can
		case main <- report:
		// and when can't, send semaphore to full to inform next level reporter
		// to switch channels' usage, and swap the two at our side
		default:
			main, full = full, main
			if len(main) == 0 {
				main <- report
			} else {
				logW("Failed issue resend %s report of %s, congested, discard.\n", report.Type, report.Target)
				warnCongested()
			}
		}
	}
}

func flipFlopSender(client *http.Client, report *ReportContainer,
	main, full chan *ReportContainer) (chan *ReportContainer, chan *ReportContainer) {
	logD("Resending %s report of %s\n", report.Type, report.Target)
	resp, err := client.Do(requestBuilder(report))
	if netErr, ok := err.(net.Error); ok {
		logI("Failed sending %s report of %s, network error: %s. issue resend.\n", report.Type, report.Target, netErr)
		select {
		// send failed report to main channel if can
		case main <- report:
		// and when can't, send semaphore to full to inform next level reporter
		// to switch channels' usage, and swap the two at our side
		default:
			main, full = full, main
			if len(main) == 0 {
				main <- report
			} else {
				logW("Failed issue resend %s report of %s, congested, discard.\n", report.Type, report.Target)
				warnCongested()
			}
		}
	} else if err != nil {
		logW("Failed resending %s report of %s, unrecoverable error: %s\n", report.Type, report.Target, err)
	} else {
		if resp.StatusCode != 200 {
			errByte, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				logW("Failed sending %s report of %s, HTTP Status %d, failed reading response body: \n", report.Type,
					report.Target, resp.StatusCode, err)
				return main, full
			}
			errSrv := &ErrResponse{}
			err = json.Unmarshal(bytes.Trim(errByte, "\x00"), errSrv)
			if err != nil {
				logW("Failed sending %s report of %s, HTTP Status %d: %s\n", report.Type,
					report.Target, resp.StatusCode, string(bytes.Trim(errByte, "\x00")))
			} else {
				logW("Failed sending %s report of %s, HTTP Status %d: %s\n", report.Type,
					report.Target, resp.StatusCode, errSrv.Msg)
			}
		} else {
			// Drain the Body to enable Keep-Alive
			_, _ = io.Copy(ioutil.Discard, resp.Body)
		}
	}
	return main, full
}

func DrainTrash(channels ...chan *ReportContainer) {
	for _, channel := range channels {
		go func() {
			for {
				report := <-channel
				logW("Trash %s report of %s. Max retry exceed. Discard.\n", report.Type, report.Target)
			}
		}()
	}
}
