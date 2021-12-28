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
	"syscall"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/tidwall/rhh"
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

type Instance struct {
	wg   sync.WaitGroup
	num  int
	size int
	path string
	cmd  *exec.Cmd
}

func (i *Instance) PID() int {
	return i.cmd.Process.Pid
}

func startInstance(num, size int, wg *sync.WaitGroup) *Instance {
	output := os.Getenv("OUTPUT_LOGS") != ""
	inst := &Instance{num: num, size: size}
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

type TestClusterContext interface {
	Monitor(size int)
	Start(size, numClients int, insts []*Instance)
	ExecClient(size int, clientNum int, c *TestConn)
}

func testSingleCluster(size int, ctx TestClusterContext, numClients int) {
	killAll()
	wlog("::CLUSTER::BEGIN::Size=%d", size)
	insts := make([]*Instance, size)
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
	ctx.Start(size, numClients, insts)
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

func (ctx *noopTestCluster) Start(size int, numClients int, insts []*Instance) {
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
	keys        rhh.Map
}

func newBasicTestCluster() *basicTestCluster {
	return &basicTestCluster{
		secsRunTime: 10,
	}
}

func (ctx *basicTestCluster) Start(size int, numClients int, insts []*Instance) {
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

func (ctx *leaderAdvertiseTestCluster) Start(size int, numClients int, insts []*Instance) {
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

// Server shutdown signal test
const slowresponse_app string = `
package main

import (
	"github.com/tidwall/uhaha"
	"time"
	"os"
	"syscall"
)

type data struct {
	counter int
}

func main() {
	var conf uhaha.Config
	conf.Name = "slowresp"
	conf.InitialData = new(data)
	conf.AddWriteCommand("incre", cmdINCRE)
	conf.ServerShutdownSignals = []os.Signal{syscall.SIGUSR1}
	conf.LeadershipTransferSignals = []os.Signal{syscall.SIGCONT}
	uhaha.Main(conf)
}

func cmdINCRE(m uhaha.Machine, args []string) (interface{}, error) {
	data := m.Data().(*data)
	data.counter += 1
	time.Sleep(time.Second * 5)
	return data.counter, nil
}
`

func buildSlowResponseApp(t *testing.T) {
	temp, err := ioutil.TempFile("", "testslowapp*.go")
	if err != nil {
		wlog("::BUILD::FAIL::\n%s\n", err.Error())
		badnews()
	}
	if _, err := temp.WriteString(slowresponse_app); err != nil {
		wlog("::BUILD::FAIL::\n%s\n", err.Error())
		badnews()
	}
	temp.Close()

	t.Cleanup(func() {
		os.Remove(temp.Name())
	})

	run("go", "build", "-o",
		filepath.Join("testing", app), temp.Name())

	wlog("::BUILD::Ok\n")
}

func testSlowResponseClusters(t *testing.T, sizes []int, numClients int,
	newCtx func() TestClusterContext,
) {
	genSeed()
	must(nil, os.MkdirAll("testing", 0777))
	verifyGoVersion()
	buildSlowResponseApp(t)
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			testSingleCluster(size, newCtx(), numClients)
		})
	}
}

type gracefullyShutdownCluster struct {
	mu    sync.Mutex
	pids  []int
	incre int
}

func newGracefullyShutdownCluster() *gracefullyShutdownCluster {
	return &gracefullyShutdownCluster{}
}

func (ctx *gracefullyShutdownCluster) Start(size, numClients int, insts []*Instance) {
	pids := make([]int, len(insts))
	for i, ins := range insts {
		pids[i] = ins.PID()
	}

	wlog(
		"::CLUSTER::Run %d servers (GracefullyShutdown) pid=%v",
		size,
		pids,
	)
	ctx.mu.Lock()
	ctx.pids = pids
	ctx.mu.Unlock()
}

func (ctx *gracefullyShutdownCluster) Monitor(size int) {
	time.Sleep(time.Second)

	numKillServer := size / 2
	killPids := make([]int, 0, numKillServer)
	for i := 0; i < numKillServer; i += 1 {
		ctx.mu.Lock()
		pid := ctx.pids[0]
		ctx.pids = ctx.pids[1:]
		ctx.mu.Unlock()

		p, err := os.FindProcess(pid)
		if err != nil {
			wlog("::CLUSTER::Process %d not found(%s)", pid, err.Error())
			badnews()
		}
		defer p.Release()

		if err := p.Signal(syscall.SIGUSR1); err != nil {
			wlog("::CLUSTER::Process %d signal err=%s", pid, err.Error())
			badnews()
		}

		wlog("::CLUSTER::Process %d sent signal", pid)
		killPids = append(killPids, pid)
	}
}

func (ctx *gracefullyShutdownCluster) ExecClient(size int, clientNum int, c *TestConn) {
	start := time.Now()
	reply, err := redis.Int(c.Do("INCRE"), nil)
	if err != nil {
		wlog("::CLIENT::Error %s", err.Error())
		badnews()
	}
	wlog("::CLIENT::slowreq reply=%d elapse=%s", reply, time.Since(start))

	ctx.mu.Lock()
	ctx.incre += 1
	ctx.mu.Unlock()
}

type shutdownSignalRemoveServerCluster struct {
	pids []int
}

func newShutdownSignalRemoveServerCluster() *shutdownSignalRemoveServerCluster {
	return &shutdownSignalRemoveServerCluster{}
}

func (ctx *shutdownSignalRemoveServerCluster) Start(size, numClients int, insts []*Instance) {
	pids := make([]int, len(insts))
	for i, ins := range insts {
		pids[i] = ins.PID()
	}

	wlog(
		"::CLUSTER::Run %d servers (ShutdownSignalRemoveServer) pid=%v",
		size,
		pids,
	)

	ctx.pids = pids
}

func (ctx *shutdownSignalRemoveServerCluster) Monitor(size int) {
	conns := make([]redis.Conn, size)
	for i := 0; i < size; i += 1 {
		addr := fmt.Sprintf(":3300%d", i+1)
		conn, err := redis.Dial("tcp", addr)
		if err != nil {
			wlog("::CLIENT::DialError %s", err)
			badnews()
		}
		conns[i] = conn
	}
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()

	for _, conn := range conns {
		reply, err := redis.String(conn.Do("PING"))
		if err != nil {
			wlog("::CLIENT::%s", err)
			badnews()
		}
		if reply != "PONG" {
			wlog("::CLIENT::Expected 'PONG' got '%s'", reply)
			badnews()
		}
	}

	checkServerList := func(clusterSize int) {
		lastServerList := ""
		for _, conn := range conns {
			reply, err := conn.Do("RAFT", "SERVER", "LIST")
			if err != nil {
				wlog("::CLIENT::%s", err)
				badnews()
			}
			replies, ok := reply.([]interface{})
			if ok != true {
				wlog("::CLIENT::RAFT_SERVER_LIST empty")
				badnews()
			}

			list := make([]string, len(replies))
			for i, r := range replies {
				s, err := redis.Strings(r, nil)
				if err != nil {
					wlog("::CLIENT::%s", err)
					badnews()
				}
				list[i] = strings.Join(s, " ")
			}
			current := strings.Join(list, ",")

			if lastServerList == "" {
				lastServerList = current
			}
			if lastServerList != current {
				wlog(
					"::CLIENT::RAFT_SERVER_LIST not same %s <> %s",
					lastServerList,
					current,
				)
				badnews()
			}
			if len(list) != clusterSize {
				wlog(
					"::CLUSTER::SIZE unexpected size: %d != %d",
					clusterSize,
					len(list),
				)
			}
		}

		wlog(
			"::RAFT_SERVER_LIST current=%s",
			lastServerList,
		)
	}

	checkServerList(size)

	pid := ctx.pids[0]
	p, err := os.FindProcess(pid)
	if err != nil {
		wlog("::CLUSTER::Process %d not found(%s)", pid, err.Error())
		badnews()
	}
	defer p.Release()

	if err := p.Signal(syscall.SIGUSR1); err != nil {
		wlog("::CLUSTER::Process %d signal err=%s", pid, err.Error())
		badnews()
	}

	conns = conns[1:]

	// wait sync
	time.Sleep(time.Second * 10)

	checkServerList(size - 1)
}

func (ctx *shutdownSignalRemoveServerCluster) ExecClient(size int, clientNum int, c *TestConn) {
}

func TestServerShutdownSignal(t *testing.T) {
	testSlowResponseClusters(t,
		[]int{5, 3, 1},
		10,
		func() TestClusterContext { return newGracefullyShutdownCluster() },
	)
	testSlowResponseClusters(t,
		[]int{5, 3},
		10,
		func() TestClusterContext { return newShutdownSignalRemoveServerCluster() },
	)
}

type leaderTransferCluster struct {
	pids []int
}

func newLeaderTransferCluster() *leaderTransferCluster {
	return &leaderTransferCluster{}
}

func (ctx *leaderTransferCluster) Start(size, numClients int, insts []*Instance) {
	pids := make([]int, len(insts))
	for i, ins := range insts {
		pids[i] = ins.PID()
	}

	wlog(
		"::CLUSTER::Run %d servers (LeaderTransfer) pid=%v",
		size,
		pids,
	)

	ctx.pids = pids
}

func (ctx *leaderTransferCluster) Monitor(size int) {
	conns := make([]redis.Conn, size)
	for i := 0; i < size; i += 1 {
		addr := fmt.Sprintf(":3300%d", i+1)
		conn, err := redis.Dial("tcp", addr)
		if err != nil {
			wlog("::CLIENT::DialError %s", err)
			badnews()
		}
		conns[i] = conn
	}
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()

	for _, conn := range conns {
		reply, err := redis.String(conn.Do("PING"))
		if err != nil {
			wlog("::CLIENT::%s", err)
			badnews()
		}
		if reply != "PONG" {
			wlog("::CLIENT::Expected 'PONG' got '%s'", reply)
			badnews()
		}
	}

	checkLeader := func() string {
		lastLeader := ""
		for _, conn := range conns {
			reply, err := redis.String(conn.Do("RAFT", "LEADER"))
			if err != nil {
				wlog("::CLIENT::%s", err)
				badnews()
			}
			if lastLeader == "" {
				lastLeader = reply
			}

			if lastLeader != reply {
				wlog(
					"::CLIENT::RAFT_LEADER not same %s <> %s",
					lastLeader,
					reply,
				)
				badnews()
			}
		}
		wlog(
			"::RAFT_LEADER current=%s",
			lastLeader,
		)
		return lastLeader
	}

	leader1 := checkLeader()

	for _, pid := range ctx.pids {
		p, err := os.FindProcess(pid)
		if err != nil {
			wlog("::CLUSTER::Process %d not found(%s)", pid, err.Error())
			badnews()
		}
		defer p.Release()

		if err := p.Signal(syscall.SIGCONT); err != nil {
			wlog("::CLUSTER::Process %d signal err=%s", pid, err.Error())
			badnews()
		}
	}

	// wait sync
	time.Sleep(time.Second * 10)

	leader2 := checkLeader()

	if leader1 == leader2 {
		wlog("::CLUSTER::LeadershipTransfer failed %s == %s", leader1, leader2)
	}
}

func (ctx *leaderTransferCluster) ExecClient(size int, clientNum int, c *TestConn) {
}

// Server leader transfer  signal test
func TestServerLeaderTransfer(t *testing.T) {
	testSlowResponseClusters(t,
		[]int{3, 5},
		10,
		func() TestClusterContext { return newLeaderTransferCluster() },
	)
}
