package uhaha

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/tidwall/rhh"
)

const app = "6a8d5bef.app"
const doTimeout = 30 * time.Second

func wlog(format string, args ...interface{}) {
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	fmt.Printf("%s\n", line)
}

func must(v interface{}, err error) interface{} {
	if err != nil {
		panic(err.Error())
	}
	return v
}

func run(cmd string, args ...string) string {
	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		if len(out) != 0 {
			wlog("::RUN::FAIL::\n%s\n", string(out))
		} else {
			wlog("::RUN::FAIL::%s\n", err)
		}
		badnews()
	}
	return strings.TrimSpace(string(out))
}

func badnews() {
	println("bad news")
	os.Exit(1)
}

func killAll() {
	for strings.Contains(run("ps"), app) {
		run("pkill", "-9", app)
	}
}

func verifyGoVersion() {
	lvers := "go version " +
		runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
	rvers := run("go", "version")
	if rvers != lvers {
		wlog("::VERSION::Mismatch::'%s' != '%s'", rvers, lvers)
		badnews()
	}
	wlog("::VERSION::Ok\n")
}

func buildTestApp() {
	run("go", "build", "-o",
		filepath.Join("testing", app), "examples/kvdb/main.go")
	wlog("::BUILD::Ok\n")
}

func genSeed() {
	seed := time.Now().UnixNano()
	sseed := os.Getenv("SEED")
	if sseed != "" {
		seed, _ = strconv.ParseInt(sseed, 10, 64)
	}
	wlog("::SEED::%d", seed)
	rand.Seed(seed)
}

func TestClusters(t *testing.T) {
	genSeed()
	must(nil, os.MkdirAll("testing", 0777))
	verifyGoVersion()
	buildTestApp()
	sizes := []int{1, 3, 5}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			testCluster(size)
		})
	}
}

func isNotLeaderErr(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "MOVED ")
}

type instance struct {
	wg   sync.WaitGroup
	num  int
	size int
	path string
	cmd  *exec.Cmd
}

func startInstance(num, size int, wg *sync.WaitGroup) *instance {
	output := os.Getenv("OUTPUT_LOGS") != ""
	inst := &instance{num: num, size: size}
	inst.wg.Add(1)
	path, err := ioutil.TempDir("", "")
	if err != nil {
		panic(err)
	}
	inst.path = path
	appPath := must(filepath.Abs(filepath.Join("testing", app))).(string)
	inst.cmd = exec.Command(appPath,
		"-t", fmt.Sprintf("%d", num),
		"-a", ":33000",
	)
	inst.cmd.Dir = path
	rerr := must(inst.cmd.StderrPipe()).(io.ReadCloser)
	rout := must(inst.cmd.StdoutPipe()).(io.ReadCloser)
	in := must(inst.cmd.StdinPipe()).(io.WriteCloser)
	rd := io.MultiReader(rerr, rout)

	must(nil, inst.cmd.Start())
	readyCh := make(chan bool, 2)
	go func() {
		f := must(
			os.Create(fmt.Sprintf("testing/%d_%d.log", num, size)),
		).(*os.File)
		defer func() {
			f.Close()
			rerr.Close()
			rout.Close()
			in.Close()
			inst.cmd.Wait()
			wg.Done()
			inst.wg.Done()
		}()
		brd := bufio.NewReader(rd)
		for {
			line, err := brd.ReadString('\n')
			if err != nil {
				wlog("::INST::%d/%d::ERROR::%s", num, size, err)
				badnews()
			}
			if output {
				os.Stdout.WriteString(line)
			}
			line = strings.TrimSpace(line)
			if strings.Contains(line, "logs loaded: ready for commands") {
				readyCh <- true
				if output {
					io.Copy(os.Stdout, brd)
				} else {
					io.Copy(ioutil.Discard, brd)
				}
				break
			}
		}
	}()
	ready := false
	tick := time.NewTicker(time.Second * 10)
	for !ready {
		select {
		case <-readyCh:
			ready = true
		case <-tick.C:
			wlog("::INST::%d/%d::TIMEOUT", num, size)
			badnews()
		}
	}
	wlog("::INST::%d/%d::Started", num, size)
	return inst
}

func randStr(n int) string {
	bytes := make([]byte, n)
	rand.Read(bytes)
	for i := 0; i < n; i++ {
		bytes[i] = 'a' + (bytes[i] % 26)
	}
	return string(bytes)
}

func testCluster(size int) {
	killAll()
	wlog("::CLUSTER::BEGIN::Size=%d", size)
	insts := make([]*instance, size)
	var wg sync.WaitGroup
	wg.Add(size)
	defer func() {
		for _, inst := range insts {
			if inst != nil {
				inst.cmd.Process.Kill()
				inst.wg.Wait()
			}
		}
		wg.Wait()
		wlog("::CLUSTER::END::Size=%d", size)
	}()
	for i := 0; i < size; i++ {
		insts[i] = startInstance(i+1, size, &wg)
	}
	var mu sync.Mutex
	var ded bool
	var cwg sync.WaitGroup
	cwg.Add(1)
	go runClients(size, &cwg, &mu, &ded)
	// go runChaos(size, insts, &wg, &cwg, &mu, &ded)
	cwg.Wait()
}

func runClients(size int, wg *sync.WaitGroup, mu *sync.Mutex, ded *bool) {
	defer wg.Done()
	const S = 10
	T := time.Second * S
	C := 50
	wlog("::CLUSTER::Run %d clients for %d seconds", C, S)

	var keys rhh.Map
	var set, deleted int
	var wg2 sync.WaitGroup
	wg2.Add(C)
	for i := 0; i < C; i++ {
		go execClient(&wg2, size, T, &keys, &set, &deleted, mu)
	}

	for i := 0; i < S; i++ {
		time.Sleep(time.Second)
		mu.Lock()
		wlog("::RUNNING::%d/%d::%d SET::%d DEL", i+1, S, set, deleted)
		mu.Unlock()
	}
	wg2.Wait()
	mu.Lock()
	*ded = true
	mu.Unlock()
}

func runChaos(
	size int, insts []*instance, wg, cwg *sync.WaitGroup,
	mu *sync.Mutex, ded *bool,
) {
	defer cwg.Done()
	if size == 1 {
		return
	}
	// chaos is pretty much taking servers down and bringing them back up.
	for {
		mu.Lock()
		if *ded {
			mu.Unlock()
			break
		}
		mu.Unlock()

		i := rand.Int() % size
		num := i + 1
		wlog("::INST::%d/%d::TAKEDOWN", num, size)
		insts[i].cmd.Process.Kill()
		insts[i].wg.Wait()
		wg.Add(1)
		wlog("::INST::%d/%d::BRINGUP", num, size)
		insts[i] = startInstance(num, size, wg)

	}
}

type tconn struct {
	conn redis.Conn
	size int
}

func openConn(size int) *tconn {
	c := &tconn{size: size}
	start := time.Now()
	for {
		addr := fmt.Sprintf(":3300%d", (rand.Int()%size)+1)
		var err error
		c.conn, err = redis.Dial("tcp", addr)
		if err == nil {
			var reply string
			reply, err = redis.String(c.conn.Do("PING"))
			if err == nil {
				if reply != "PONG" {
					wlog("::CLIENT::Expected 'PONG' got '%s'", reply)
					badnews()
				}
				break
			}
		}
		if time.Since(start) > time.Second*10 {
			wlog("::CLIENT::%s", err)
			badnews()
		}
	}
	return c
}

func (c *tconn) do(cmd string, args ...interface{}) interface{} {
	start := time.Now()
	for {
		reply, err := c.conn.Do(cmd, args...)
		if err != nil {
			if time.Since(start) > time.Second*10 {
				wlog("::CLIENT::%s", err)
				badnews()
			}
			// if isNotLeaderErr(err) {
			c.conn.Close()
			nc := openConn(c.size)
			c.conn = nc.conn
			continue
		}
		return reply
	}
}

func execClient(
	wg *sync.WaitGroup, size int, dur time.Duration,
	keys *rhh.Map, set, deleted *int, mu *sync.Mutex,
) {

	defer wg.Done()
	start := time.Now()
	c := openConn(size)
	defer func() {
		if c.conn != nil {
			c.conn.Close()
		}
	}()
	for time.Since(start) < dur {
		// Set a random key
		{
			key := randStr(32)
			reply, err := redis.String(c.do("SET", key, key), nil)
			if reply != "OK" {
				fmt.Printf("%v\n", err)
				// continue
				wlog("::CLIENT::Invalid reply '%s'", reply)
				badnews()
			}
			mu.Lock()
			keys.Set(key, true)
			(*set)++
			mu.Unlock()

		}
		// Del a random key
		{
			mu.Lock()
			key, _, _ := keys.GetPos(rand.Uint64())
			mu.Unlock()
			reply, _ := redis.Int(c.do("DEL", key), nil)
			if reply != 1 && reply != 0 {
				wlog("::CLIENT::Invalid reply '%d'", reply)
				badnews()
			}
			mu.Lock()
			*deleted += int(reply)
			mu.Unlock()
		}
	}
}
