# Planet
Component of StarPing project.

Planet part sends packets to targets, retrieve latency and route data, and
report to Star. A Planet works for one Star, and one Star can have multiple
Planets, as name indicates.

Planet is designed to work without system-depend commands but send ICMP packets
itself. To do so, proper permission shall be grant on some platforms.
On Linux, root privilege is required.

## Compile
```bash
git clone https://github.com/tongyuantongyu/StarPing-Planet.git
cd StarPing-Planet/cmd/planet
go build planet.go
```

## Binary
You can get prebuilt binary [here](https://github.com/tongyuantongyu/StarPing-Planet/releases).
Note: these binaries have dbg symbol cut to reduce size. If you don't want that,
you can build yourself.