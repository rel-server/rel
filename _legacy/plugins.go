package main

import (
	"log"
	"os"
	"path"
	"plugin"
	"strings"

	"sales-way.com/server/sw"
)

var dirs = []string{"/plugins", "./plugins", getenvOrDefault("SW_PLUGINS_DIR")}

func registerPlugins(srv *sw.SwServer) {
	var plugins = make([]string, 0)

	// Récupération des fichiers qui sont probablement des plugins go
	for _, dir := range dirs {
		if dir == "" {
			continue
		}

		if files, err := os.ReadDir(dir); err == nil {
			for _, file := range files {
				if file.IsDir() || !strings.HasSuffix(file.Name(), ".so") {
					// Ce n'est vraisemblablement pas un plugin
					continue
				}
				// On ajoute le chemin pour joindre le plugin
				plugins = append(plugins, path.Join(dir, file.Name()))
			}
		}
	}

	// Enfin, on les ouvre
	for _, plug := range plugins {
		var opened_plugin, err = plugin.Open(plug)
		if err != nil {
			log.Print("!! ", plug, ": ", err)
			continue
		}

		sym, err := opened_plugin.Lookup("SwInit")
		if err != nil {
			log.Print("!! ", plug, ": ", err)
			continue
		}

		fn, ok := sym.(func(*sw.SwServer) error)
		if !ok {
			log.Print("!! ", plug, " SwInit is not of type (func (srv *sw.SwServer) error)")
			continue
		}

		srv.LogInfo("loading plugin '", plug, "'")
		err = fn(srv)
		if err != nil {
			log.Print("!! ", plug, ": ", err)
		}
	}
}
