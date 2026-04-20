package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	updateManifestGitHub = "https://raw.githubusercontent.com/browserwing/browserwing/main/release-manifest/beta.json"
	updateManifestGitee  = "https://gitee.com/browserwing/browserwing/raw/main/release-manifest/beta.json"
)

func checkForUpdate() {
	ch := make(chan string, 1)
	go func() {
		latest := fetchLatestVersion()
		if latest != "" && isNewer(latest, strings.TrimPrefix(Version, "v")) {
			ch <- latest
		} else {
			ch <- ""
		}
	}()

	select {
	case v := <-ch:
		if v != "" {
			fmt.Fprintf(os.Stderr, "\n  New version available: %s (current: %s)\n", v, Version)
			fmt.Fprintf(os.Stderr, "  Update: npm install -g browserwing@latest\n")
			fmt.Fprintf(os.Stderr, "  Release: https://github.com/browserwing/browserwing/releases\n\n")
		}
	case <-time.After(2 * time.Second):
	}
}

func fetchLatestVersion() string {
	for _, url := range []string{updateManifestGitHub, updateManifestGitee} {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &manifest) == nil && manifest.Version != "" {
			return manifest.Version
		}
	}
	return ""
}

func isNewer(remote, local string) bool {
	rParts := parseSemverParts(remote)
	lParts := parseSemverParts(local)

	for i := 0; i < 3; i++ {
		r, l := 0, 0
		if i < len(rParts.nums) {
			r = rParts.nums[i]
		}
		if i < len(lParts.nums) {
			l = lParts.nums[i]
		}
		if r > l {
			return true
		}
		if r < l {
			return false
		}
	}

	if rParts.pre == "" && lParts.pre != "" {
		return true
	}
	if rParts.pre != "" && lParts.pre == "" {
		return false
	}
	return rParts.pre > lParts.pre
}

type semverParts struct {
	nums []int
	pre  string
}

func parseSemverParts(v string) semverParts {
	v = strings.TrimPrefix(v, "v")
	parts := semverParts{}

	preSplit := strings.SplitN(v, "-", 2)
	if len(preSplit) == 2 {
		parts.pre = preSplit[1]
	}

	for _, s := range strings.Split(preSplit[0], ".") {
		n, _ := strconv.Atoi(s)
		parts.nums = append(parts.nums, n)
	}
	return parts
}
