// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reloadcmd

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/ceymard/dmut/v3/mutations"
	"github.com/ceymard/dmut/v3/mutations/model"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
)

// runDmut applies the mutations found in cfg.Dmut.Path, in-process, with
// the login dmutURI picks. ran is false when dmut.path is empty. logger
// receives dmut's own records and is expected to carry module=dmut.
func runDmut(ctx context.Context, cfg *config.Config, logger *slog.Logger) (ran bool, err error) {
	paths := splitPaths(cfg.Dmut.Path)
	if len(paths) == 0 {
		return false, nil
	}

	uri, uerr := dmutURI(cfg)
	if uerr != nil {
		return true, oops.Wrapf(uerr, "building the dmut connection string")
	}

	timeout := time.Duration(cfg.Dmut.Timeout) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rerr := mutations.Apply(runCtx, uri, paths, mutations.Options{
		Mode:   dmutMode(cfg.Dmut.Mode),
		Commit: true,
		Logger: logger,
	})
	if rerr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return true, oops.Errorf("dmut timed out after %s", timeout)
		}
		return true, oops.With("path", cfg.Dmut.Path).Wrapf(rerr, "dmut failed")
	}
	return true, nil
}

// dmutMode maps rel's dmut.mode values onto dmut 3's modes: default is
// normal, simple is safe and full is force.
func dmutMode(mode string) model.Mode {
	switch mode {
	case config.DmutModeSimple:
		return model.ModeSafe
	case config.DmutModeFull:
		return model.ModeForce
	}
	return model.ModeNormal
}

// splitPaths splits a colon-separated list, dropping empty entries.
func splitPaths(list string) []string {
	var out []string
	for _, p := range strings.Split(list, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dmutURI is cfg.Pg.URI with its login replaced by dmut.user/dmut.password,
// each falling back on its own to pg.user/pg.password when empty.
func dmutURI(cfg *config.Config) (string, error) {
	u, err := url.Parse(cfg.Pg.URI)
	if err != nil {
		return "", err
	}
	user, password := cfg.Dmut.User, cfg.Dmut.Password
	if user == "" {
		user = cfg.Pg.User
	}
	if password == "" {
		password = cfg.Pg.Password
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}
