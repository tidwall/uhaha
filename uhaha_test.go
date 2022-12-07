package uhaha

import (
	"bufio"
	"fmt"
	"io"
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
	"github.com/tidwall/hashmap"
)

const app = "6a8d5bef.app"

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
	path, err := os.MkdirTemp("", "")
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
					io.Copy(io.Discard, brd)
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

type TestClusterContext interface {
	Monitor(size int)
	Start(size, numClients int)
	ExecClient(size int, clientNum int, c *TestConn)
}

func testSingleCluster(size int, ctx TestClusterContext, numClients int) {
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
	ctx.Start(size, numClients)
	var wg2 sync.WaitGroup
	wg2.Add(numClients)
	for i := 0; i < numClients; i++ {
		go func(i int) {
			defer wg2.Done()
			c := OpenTestConn(size)
			defer func() {
				if c.conn != nil {
					c.conn.Close()
				}
			}()
			ctx.ExecClient(size, i, c)
		}(i)
	}
	ctx.Monitor(size)
	wg2.Wait()
}

type TestConn struct {
	conn redis.Conn
	size int
}

func OpenTestConn(size int) *TestConn {
	c := &TestConn{size: size}
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

func (c *TestConn) Do(cmd string, args ...interface{}) interface{} {
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
			nc := OpenTestConn(c.size)
			c.conn = nc.conn
			continue
		}
		return reply
	}
}

func testClusters(t *testing.T, sizes []int, numClients int,
	newCtx func() TestClusterContext,
) {
	genSeed()
	must(nil, os.MkdirAll("testing", 0777))
	verifyGoVersion()
	buildTestApp()
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			testSingleCluster(size, newCtx(), numClients)
		})
	}
}

// NOOP TEST

type noopTestCluster struct {
}

func newNoopTestCluster() *noopTestCluster {
	return &noopTestCluster{}
}

func (ctx *noopTestCluster) Start(size int, numClients int) {
	wlog("::CLUSTER::Run %d clients (NOOP)", numClients)
}

func (ctx *noopTestCluster) Monitor(size int) {}

func (ctx *noopTestCluster) ExecClient(size int, clientNum int, c *TestConn) {}

func TestNoopCluster(t *testing.T) {
	testClusters(t,
		[]int{1, 3, 5}, // sizes
		50,             // clients
		func() TestClusterContext { return newNoopTestCluster() },
	)

}

// BASIC TEST

type basicTestCluster struct {
	secsRunTime int
	mu          sync.Mutex
	set         int
	deleted     int
	keys        hashmap.Map[string, interface{}]
}

func newBasicTestCluster() *basicTestCluster {
	return &basicTestCluster{
		secsRunTime: 10,
	}
}

func (ctx *basicTestCluster) Start(size int, numClients int) {
	wlog("::CLUSTER::Run %d clients for %d seconds",
		numClients, ctx.secsRunTime)
}

func (ctx *basicTestCluster) Monitor(size int) {
	for i := 0; i < ctx.secsRunTime; i++ {
		time.Sleep(time.Second)
		ctx.mu.Lock()
		wlog("::RUNNING::%d/%d::%d SET::%d DEL", i+1,
			ctx.secsRunTime, ctx.set, ctx.deleted)
		ctx.mu.Unlock()
	}
}

func (ctx *basicTestCluster) ExecClient(size int, clientNum int, c *TestConn) {
	dur := time.Second * time.Duration(ctx.secsRunTime)
	start := time.Now()
	for time.Since(start) < dur {
		// Set a random key
		{
			key := randStr(32)
			reply, err := redis.String(c.Do("SET", key, key), nil)
			if reply != "OK" {
				fmt.Printf("%v\n", err)
				// continue
				wlog("::CLIENT::Invalid reply '%s'", reply)
				badnews()
			}
			ctx.mu.Lock()
			ctx.keys.Set(key, true)
			ctx.set++
			ctx.mu.Unlock()

		}
		// Del a random key
		{
			ctx.mu.Lock()
			key, _, _ := ctx.keys.GetPos(rand.Uint64())
			ctx.mu.Unlock()
			reply, _ := redis.Int(c.Do("DEL", key), nil)
			if reply != 1 && reply != 0 {
				wlog("::CLIENT::Invalid reply '%d'", reply)
				badnews()
			}
			ctx.mu.Lock()
			ctx.deleted += int(reply)
			ctx.mu.Unlock()
		}
	}
}

func TestClusters(t *testing.T) {
	testClusters(t,
		[]int{1, 3, 5}, // sizes
		50,             // clients
		func() TestClusterContext { return newBasicTestCluster() },
	)
}

// LEADER ADVERTISE TEST

type leaderAdvertiseTestCluster struct {
}

func newLeaderAdvertiseTestCluster() *leaderAdvertiseTestCluster {
	return &leaderAdvertiseTestCluster{}
}

func (ctx *leaderAdvertiseTestCluster) Start(size int, numClients int) {
	wlog("::CLUSTER::Run %d clients (LeaderAdvertise)", numClients)
}

func (ctx *leaderAdvertiseTestCluster) Monitor(size int) {}

func (ctx *leaderAdvertiseTestCluster) ExecClient(size int, clientNum int,
	c *TestConn,
) {
	out := c.Do("RAFT", "LEADER")
	got := fmt.Sprintf("%s", out)
	expect := "0.0.0.0:3300"
	if !strings.HasPrefix(got, expect) {
		panic(fmt.Sprintf("mismatch: got '%s', expected prefix '%s*'", got,
			expect))
	}
}

func TestLeaderAdvertise(t *testing.T) {
	testClusters(t,
		[]int{1, 3, 5}, // sizes
		50,             // clients
		func() TestClusterContext { return newLeaderAdvertiseTestCluster() },
	)
}
