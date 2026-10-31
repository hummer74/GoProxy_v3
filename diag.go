// diag.go — GoProxy -diag: сбор диагностики в x_diag.txt
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	windows "golang.org/x/sys/windows"
)

// runDiagMode собирает config + ssh -V + ssh -G + netstat + хвост логов.
func runDiagMode() {
	execPath, _ := os.Executable()
	workDir := filepath.Dir(execPath)
	cfgPath := filepath.Join(workDir, "GoProxy.ini")
	_ = LoadConfig(cfgPath)

	out := filepath.Join(workDir, "x_diag.txt")
	f, err := os.Create(out)
	if err != nil {
		fmt.Printf("diag: cannot create %s: %v\n", out, err)
		return
	}
	defer f.Close()

	w := func(format string, args ...interface{}) {
		fmt.Fprintf(f, format+"\n", args...)
	}
	w("=== GoProxy diag %s ===", time.Now().Format("2006-01-02 15:04:05"))
	w("exe: %s", execPath)
	w("workdir: %s", Config.Paths.WorkDir)
	w("proxy=%d pac=%d socksInterval=%d failoverResp=%d",
		Config.Network.ProxyPort, Config.Network.PACHttpPort,
		Config.Network.SocksCheckInterval, Config.General.FailoverResponseTime)
	w("checkURLs: %s", strings_Join(internetCheckURLs(), " | "))
	w("hostsCheck=%d origCheck=%d smart=%v returnOrig=%v",
		Config.General.HostsCheckInterval, Config.General.OriginalHostCheck,
		Config.General.SmartFailover, Config.General.ReturnToOriginalHost)

	// ssh -V
	w("\n--- ssh -V ---")
	cmd := exec.Command("ssh", "-V")
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if out2, err := cmd.CombinedOutput(); err != nil {
		w("err: %v out: %s", err, string(out2))
	} else {
		w("%s", string(out2))
	}

	// state
	w("\n--- state ---")
	if st, err := LoadState(); err != nil {
		w("LoadState err: %v", err)
	} else {
		w("host=%s chain=%v failover=%v orig=%s", st.Host, st.IsChain, st.IsFailoverActive, st.OriginalHost)
	}
	host, active, _, fs := connState.GetAll()
	w("connState: host=%s active=%v fail=%s", host, active, fs.String())
	w("socks handshake 1080: %v", checkSocks5(Config.Network.ProxyPort))
	w("internet direct: %v", checkInternetDirect())

	// netstat
	w("\n--- netstat LISTEN 1080/8080 ---")
	nsc := exec.Command("cmd", "/c", fmt.Sprintf("netstat -ano | findstr :%d | findstr :%d", Config.Network.ProxyPort, Config.Network.PACHttpPort))
	nsc.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if out2, _ := nsc.CombinedOutput(); len(out2) > 0 {
		w("%s", string(out2))
	} else {
		// fallback по отдельности
		for _, p := range []int{Config.Network.ProxyPort, Config.Network.PACHttpPort} {
			c2 := exec.Command("cmd", "/c", fmt.Sprintf("netstat -ano | findstr :%d", p))
			c2.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
			o, _ := c2.CombinedOutput()
			w("port %d:\n%s", p, string(o))
		}
	}

	// хвосты логов
	w("\n--- log tails ---")
	for _, lf := range []string{"x_goproxy_debug.log", "x_goproxy_tunnel.log", "x_goproxy_ssh.log"} {
		p := filepath.Join(workDir, lf)
		data, err := os.ReadFile(p)
		if err != nil {
			w("%s: (no file)", lf)
			continue
		}
		if len(data) > 8000 {
			data = data[len(data)-8000:]
		}
		w("--- %s (tail) ---\n%s", lf, string(data))
	}

	fmt.Printf("Diag written: %s\n", out)
}

func strings_Join(a []string, sep string) string {
	out := ""
	for i, s := range a {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
