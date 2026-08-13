package main

import (
	"os"
	"regexp"

	"sales-way.com/server/sw"
)

const VERSION = "0.7.6"

var clientVersion = ""

func getClientVersion(srv *sw.SwServer) {
	filename := "/static/app.js"
	stat, err := os.Stat(filename)
	if err != nil {
		srv.LogWarn(err)
		return
	}
	length := stat.Size()

	f, err := os.Open("/static/app.js")
	if err != nil {
		srv.LogWarn("impossible to read client file ", err)
		return
	}
	defer f.Close()

	seeksize := length - 64
	if seeksize > 0 {
		// log.Print("seek ", seeksize)
		_, _ = f.Seek(seeksize, 0)
	} else {
		seeksize = 0
	}
	buf := make([]byte, length-seeksize)
	_, err = f.Read(buf)
	if err != nil {
		return
	}

	regexp := regexp.MustCompile("//!(v[^\n]+)")
	match := regexp.FindStringSubmatch(string(buf))

	if match != nil {
		clientVersion = match[1]
		srv.LogInfo("client version is ", clientVersion)
	}
}
