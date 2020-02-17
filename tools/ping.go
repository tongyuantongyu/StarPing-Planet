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

package tools

import (
    "fmt"
    "math"
    "net"
    "starping/network"
    "time"
)

// PingConfig represent a ping work config
type PingConfig struct {
    Frequency time.Duration `json:"frequency"`
    Interval  time.Duration `json:"interval"`
    Timeout   time.Duration `json:"timeout"`
    Count     int `json:"count"`
}

// PingStat represent a statistic data to be sent to Star
type PingStat struct {
    IP string `json:"ip"`
    Stat struct {
        Timeout bool `json:"timeout"`
        Avg float64 `json:"avg"`
        Min float64 `json:"min"`
        Max float64 `json:"max"`
        StdDev float64 `json:"std_dev"`
        Drop int `json:"drop"`
        Total int `json:"total"`
    } `json:"stat"`
}

// PingData represent raw ping data to be sent to Star
type PingData struct {
    IP string `json:"target"`
    Data []*network.Result `json:"data"`
}

func (stat *PingStat) String() string {
    if stat.Stat.Drop == stat.Stat.Total {
        return fmt.Sprintf(
            "Statistics for %s: No response from target. No statistics available. Drop/Total: %d/%d DropRate: 100%%\n",
            stat.IP, stat.Stat.Drop, stat.Stat.Total)
    }
    return fmt.Sprintf(
        "Statistics for %s: Avg: %.2fms, Min: %.2fms, Max: %.2fms, SDev: %.2fms, Drop/Total: %d/%d DropRate: %.1f%%\n",
        stat.IP, stat.Stat.Avg, stat.Stat.Min, stat.Stat.Max, stat.Stat.StdDev, stat.Stat.Drop, stat.Stat.Total,
        float64(stat.Stat.Drop * 100) / float64(stat.Stat.Total))
}

func Ping(ip string, config *PingConfig) (stat *PingStat, err error) {
    addr, err := net.ResolveIPAddr("", ip)
    if err != nil {
        return
    }
    stat = &PingStat{
        IP: addr.IP.String(),
    }
    stat.Stat.Min = math.MaxFloat64
    stat.Stat.Total = config.Count
    stat.Stat.Timeout = false
    m := network.GetICMPManager()
    for i := 0; i < config.Count; i++ {
        result := <- m.Issue(addr, 100, config.Timeout)
        if result.Code != 257 {
            stat.Stat.Drop++
        } else {
            timeFloat := float64(result.Latency) / float64(time.Millisecond)
            stat.Stat.Avg += timeFloat
            stat.Stat.Min = math.Min(stat.Stat.Min, timeFloat)
            stat.Stat.Max = math.Max(stat.Stat.Max, timeFloat)
            stat.Stat.StdDev += timeFloat * timeFloat
        }
        time.Sleep(config.Interval)
    }
    if stat.Stat.Total == stat.Stat.Drop {
        stat.Stat.Min = 0
        stat.Stat.Timeout = true
        return
    }
    succeed := float64(stat.Stat.Total - stat.Stat.Drop)
    total := float64(stat.Stat.Total)
    stat.Stat.Avg = stat.Stat.Avg / succeed
    // For standard derivation, we use D(X) = E(X^2) - E(X)^2 to compute variance,
    // And then multiply m * (n-1) / n / (m-1) (where n = total, m = succeed)
    // to estimate variance of 10 packets.
    // if this estimate got NaN or Inf, then store it as 0.
    stat.Stat.StdDev = math.Sqrt((stat.Stat.StdDev / succeed - stat.Stat.Avg * stat.Stat.Avg) * succeed * (
        total - 1) / total / (succeed - 1))
    if math.IsNaN(stat.Stat.StdDev) || math.IsInf(stat.Stat.StdDev, 1) {
        stat.Stat.StdDev = 0
    }
    return
}

func PingInfo(ip string, config *PingConfig) (stat *PingStat, err error) {
    addr, err := net.ResolveIPAddr("", ip)
    if err != nil {
        return
    }
    stat = &PingStat{
        IP: addr.IP.String(),
    }
    stat.Stat.Min = math.MaxFloat64
    stat.Stat.Total = config.Count
    m := network.GetICMPManager()
    for i := 0; i < config.Count; i++ {
        result := <- m.Issue(addr, 100, config.Timeout)
        if result.Code == 256 {
            fmt.Printf("#%2d: Timeout.\n", i+1)
            stat.Stat.Drop++
        } else if result.Code != 257 {
            info, ok := network.IcmpUnreachableMsg[result.Code]
            if !ok {
                info = fmt.Sprintf("Unknown destination unreachable code <%d>", result.Code)
            }
            fmt.Printf("#%2d: Reply from %s (%.2fms): %s.\n", i+1, result.AddrIP,
                float64(result.Latency) / float64(time.Millisecond), info)
            stat.Stat.Drop++
        } else {
            timeFloat := float64(result.Latency) / float64(time.Millisecond)
            fmt.Printf("#%2d: Reply from %s (%.2fms): Echo Reply.\n", i+1, result.AddrIP, timeFloat)
            stat.Stat.Avg += timeFloat
            stat.Stat.Min = math.Min(stat.Stat.Min, timeFloat)
            stat.Stat.Max = math.Max(stat.Stat.Max, timeFloat)
            stat.Stat.StdDev += timeFloat * timeFloat
        }
        time.Sleep(config.Interval)
    }
    if stat.Stat.Total == stat.Stat.Drop {
        stat.Stat.Min = 0
        return
    }
    succeed := float64(stat.Stat.Total - stat.Stat.Drop)
    total := float64(stat.Stat.Total)
    stat.Stat.Avg = stat.Stat.Avg / succeed
    stat.Stat.StdDev = math.Sqrt((stat.Stat.StdDev / succeed - stat.Stat.Avg * stat.Stat.Avg) * succeed * (
        total - 1) / total / (succeed - 1))
    if math.IsNaN(stat.Stat.StdDev) || math.IsInf(stat.Stat.StdDev, 1) {
        stat.Stat.StdDev = 0
    }
    return
}

func PingRaw(ip string, config *PingConfig) (data *PingData, err error) {
    addr, err := net.ResolveIPAddr("", ip)
    if err != nil {
        return
    }
    data = &PingData{
        IP: addr.IP.String(),
        Data: make([]*network.Result, config.Count),
    }
    m := network.GetICMPManager()
    for i := 0; i < config.Count; i++ {
        data.Data[i] = <- m.Issue(addr, 100, config.Timeout)
        time.Sleep(config.Interval)
    }
    return
}