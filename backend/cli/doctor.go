package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func handleDoctor() bool {
	fmt.Print(banner)
	fmt.Printf("\n  BrowserWing Doctor — %s\n\n", Version)

	allOK := true

	// 1. Server connectivity
	fmt.Print("  Checking server connectivity...")
	baseURL := getBaseURL()
	serverOK := checkServer(baseURL)
	if serverOK {
		printOK(fmt.Sprintf("Server running at %s", baseURL))
	} else {
		printFail(fmt.Sprintf("Cannot reach %s", baseURL))
		allOK = false
	}

	// 2. Chrome detection
	fmt.Print("  Checking Chrome / Chromium...")
	chromePath, chromeVer := detectChrome()
	if chromePath != "" {
		printOK(fmt.Sprintf("%s (%s)", chromeVer, chromePath))
	} else {
		printFail("Chrome not found in PATH")
		allOK = false
	}

	// 3. Script count
	if serverOK {
		fmt.Print("  Checking available scripts...")
		total := countScripts()
		if total >= 0 {
			printOK(fmt.Sprintf("%d scripts loaded", total))
		} else {
			printWarn("Could not fetch scripts")
		}
	}

	// 4. System info
	fmt.Print("  System info...")
	printOK(fmt.Sprintf("%s/%s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version()))

	// 5. Config detection
	fmt.Print("  Checking config...")
	port := detectPortFromConfig()
	if port != "" {
		printOK(fmt.Sprintf("config.toml found (config port=%s, connected via %s)", port, baseURL))
	} else {
		printWarn(fmt.Sprintf("No config.toml found, connected via %s", baseURL))
	}

	fmt.Println()
	if allOK {
		fmt.Println("  All checks passed. BrowserWing is ready.")
	} else {
		fmt.Println("  Some checks failed. See above for details.")
		fmt.Println()
		fmt.Println("  Troubleshooting:")
		fmt.Println("    - Make sure BrowserWing server is running: browserwing --port 8080")
		fmt.Println("    - Install Chrome: https://www.google.com/chrome/")
		fmt.Println("    - Full guide: https://github.com/browserwing/browserwing/blob/main/INSTALL.md")
	}
	fmt.Println()

	if !allOK {
		os.Exit(1)
	}
	return true
}

func checkServer(baseURL string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func detectChrome() (path string, version string) {
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}, candidates...)
	}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}, candidates...)
	}

	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			if _, err2 := os.Stat(c); err2 != nil {
				continue
			}
			p = c
		}
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			return p, "version unknown"
		}
		return p, strings.TrimSpace(string(out))
	}
	return "", ""
}

func countScripts() int {
	body, err := apiGet("/api/v1/scripts?page_size=1")
	if err != nil {
		return -1
	}
	var resp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return -1
	}
	return resp.Total
}

func printOK(msg string) {
	fmt.Printf(" [OK] %s\n", msg)
}

func printFail(msg string) {
	fmt.Printf(" [FAIL] %s\n", msg)
}

func printWarn(msg string) {
	fmt.Printf(" [WARN] %s\n", msg)
}
