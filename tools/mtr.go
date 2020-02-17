package tools

import (
    "fmt"
    lru "github.com/hashicorp/golang-lru"
    "math"
    "net"
    "starping/network"
    "strings"
    "sync"
    "time"
)

var IcmpUnreachableMark = map[int]string{
    0:   "!N",
    1:   "!H",
    2:   "!P",
    4:   "!F",
    5:   "!S",
    13:  "!X",
    14:  "!V",
    15:  "!C",
}

// MTRConfig represent a mtr work config
type MTRConfig struct {
    Frequency time.Duration `json:"frequency"`
    Timeout   time.Duration `json:"timeout"`
    Interval  time.Duration `json:"interval"`
    MaxTTL    int `json:"max_ttl"`
    Count     int `json:"count"`
}

type HopInfo struct {
    IP string `json:"ip"`
    RDNS string `json:"rdns"`
    Code int `json:"code"`
}

func (i *HopInfo) String() (s string) {
    s = i.IP
    if i.RDNS != "" {
        s += fmt.Sprintf("(%s)", i.RDNS)
    }
    if i.Code < 256 {
        if mark, ok := IcmpUnreachableMark[i.Code]; ok {
            s += fmt.Sprintf(" %s", mark)
        } else {
            s += fmt.Sprintf(" !<%d>", i.Code)
        }
    }
    return
}

// MTRHopStat represent statistic data of a hop to be sent to Star
type MTRHopStat struct {
    Index int `json:"index"`
    Timeout bool `json:"timeout"`
    IP []HopInfo `json:"addr"`
    Avg float64  `json:"avg"`
    Min float64 `json:"min"`
    Max float64 `json:"max"`
    StdDev float64 `json:"std_dev"`
    Drop int `json:"drop"`
    Total int `json:"total"`
}

type MTRStat struct {
    IP string `json:"ip"`
    HopCount int `json:"hop_count"`
    Stat *[]MTRHopStat `json:"stat"`
}

func (stat *MTRStat) String() (s string) {
    s += fmt.Sprintf("MTR Statistic for target %s:\n", stat.IP)
    addrWidth := 6
    for _, hop := range *stat.Stat {
        for _, ip := range hop.IP {
            if len(ip.String()) > addrWidth {
                addrWidth = len(ip.String())
            }
        }
    }
    addrString := fmt.Sprintf("%%-%ds ", addrWidth)
    s += fmt.Sprintln(" #  Address" + strings.Repeat(" ",
        addrWidth-6) + " Avg/ms  Min/ms  Max/ms SDev/ms Dr/To DRate")
    for index, hop := range *stat.Stat {
        s += fmt.Sprintf("%2d: ", index + 1)
        if hop.Timeout {
            s += fmt.Sprintln("*")
            continue
        }
        s += fmt.Sprintf(addrString, hop.IP[0].String())
        s += fmt.Sprintf("%7.2f %7.2f %7.2f %7.2f %2d/%2d %4.1f%%\n",
            hop.Avg, hop.Min, hop.Max, hop.StdDev, hop.Drop, hop.Total,
            float64(hop.Drop * 100) / float64(hop.Total))
        if len(hop.IP) > 1 {
            for _, ip := range hop.IP[1:] {
                s += fmt.Sprintf("    %s\n", ip.String())
            }
        }
    }
    return
}

type mtrHopStat struct {
    IP map[HopInfo]struct{}
    Avg float64
    Min float64
    Max float64
    StdDev float64
    Drop int
    Total int
}

var cache *lru.TwoQueueCache
var once sync.Once

func getRDNSCache() *lru.TwoQueueCache {
    once.Do(func() {
        cache, _ = lru.New2Q(8192)
    })
    return cache
}

func rDNSLookup(ip string) string {
    c := getRDNSCache()
    if entry, ok := c.Get(ip); ok {
        return entry.(string)
    } else {
        rdns, err := net.LookupAddr(ip)
        if err != nil {
            return ""
        }
        record := ""
        if len(rdns) != 0 {
            record = rdns[0][:len(rdns[0])-1]
        }
        c.Add(ip, record)
        return record
    }
}

func MTR(ip string, config *MTRConfig) (*MTRStat, error) {
    addr, err := net.ResolveIPAddr("", ip)
    if err != nil {
        return nil, err
    }
    _stat := make([]mtrHopStat, config.MaxTTL)
    minHop := config.MaxTTL
    maxHop := 0
    for i := 0; i < config.MaxTTL; i++ {
        _stat[i].Min = math.MaxFloat64
        _stat[i].IP = make(map[HopInfo]struct{})
    }
    m := network.GetICMPManager()
    for i := 0; i < config.Count; i++ {
        for j := 0; j < config.MaxTTL; j++ {
            _stat[j].Total++
            result := <- m.Issue(addr, j + 1, config.Timeout)
            time.Sleep(config.Interval)
            if result.Code == 256 {
                _stat[j].Drop++
            } else {
                _stat[j].IP[HopInfo{
                    IP:   result.AddrIP.String(),
                    Code: result.Code,
                }] = struct{}{}
                timeFloat := float64(result.Latency) / float64(time.Millisecond)
                _stat[j].Avg += timeFloat
                _stat[j].Min = math.Min(_stat[j].Min, timeFloat)
                _stat[j].Max = math.Max(_stat[j].Max, timeFloat)
                _stat[j].StdDev += timeFloat * timeFloat
                if result.Code != 258 {
                    if minHop > j {
                        minHop = j
                    }
                    if maxHop < j + 1 {
                        maxHop = j + 1
                    }
                    break
                }
            }
        }
    }
    h := make(map[string]struct{})
    // if a hop and its previous hop are identical, then this hop is
    // caused by timeout and should be trimmed
    CHECK:
    for i := maxHop - 1; i > minHop; i-- {
        // if this hop is totally time out
        if _stat[i].Drop == _stat[i].Total {
            continue
        }
        // or if each ip in this hop is identical to the previous one
        if len(_stat[i].IP) == len(_stat[i - 1].IP) {
            for k := range _stat[i].IP {
                h[k.String()] = struct{}{}
            }
            for k := range _stat[i - 1].IP {
                if _, ok := h[k.String()]; !ok {
                    break CHECK
                }
            }
            maxHop = i
        } else {
            break
        }
    }
    _stat = _stat[:maxHop]
    stat := make([]MTRHopStat, 0)
    for i := 0; i < maxHop; i++ {
        if _stat[i].Total == 0 {
            break
        }
        stat = append(stat, MTRHopStat{
            Index:   i + 1,
            Max:     _stat[i].Max,
            Drop:    _stat[i].Drop,
            Total:   _stat[i].Total,
        })
        if _stat[i].Total == _stat[i].Drop {
            stat[i].Timeout = true
            continue
        }
        stat[i].IP = make([]HopInfo, 0, len(_stat[i].IP))
        for ip := range _stat[i].IP {
            ip.RDNS = rDNSLookup(ip.IP)
            stat[i].IP = append(stat[i].IP, ip)
        }
        stat[i].Min = _stat[i].Min
        succeed := float64(_stat[i].Total - _stat[i].Drop)
        total := float64(_stat[i].Total)
        stat[i].Avg = _stat[i].Avg / succeed
        stat[i].StdDev = math.Sqrt((_stat[i].StdDev / succeed - stat[i].Avg * stat[i].Avg) * succeed * (
            total - 1) / total / (succeed - 1))
        if math.IsNaN(stat[i].StdDev) || math.IsInf(stat[i].StdDev, 1) {
            stat[i].StdDev = 0
        }
    }
    return &MTRStat{
        IP:   ip,
        Stat: &stat,
    }, nil
}