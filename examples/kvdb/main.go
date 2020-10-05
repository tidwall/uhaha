// Copyright 2020 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/match"
	"github.com/tidwall/redcon"
	"github.com/tidwall/sds"
	"github.com/tidwall/tinybtree"
	"github.com/tidwall/uhaha"
)

func main() {
	var conf uhaha.Config

	conf.Name = "kvdb"
	conf.Version = "0.0.1"
	conf.InitialData = new(database)
	conf.Snapshot = snapshot
	conf.Restore = restore
	conf.Tick = tick

	conf.AddWriteCommand("set", cmdSET)
	conf.AddWriteCommand("del", cmdDEL)
	conf.AddReadCommand("get", cmdGET)
	conf.AddReadCommand("keys", cmdKEYS)
	conf.AddReadCommand("dbsize", cmdDBSIZE)
	conf.AddIntermediateCommand("monitor", cmdMONITOR)
	conf.AddIntermediateCommand("setrandquote", cmdSETRANDQUOTE)

	uhaha.Main(conf)
}

type object struct {
	key     string
	value   string
	created int64
	expires int64
}

type database struct {
	keys tinybtree.BTree // (key)->(object)
	exps tinybtree.BTree // [exp/key]->(nil)
}

func tick(m uhaha.Machine) {
	db := m.Data().(*database)
	exkey := make([]byte, 8)
	binary.BigEndian.PutUint64(exkey, uint64(m.Now().UnixNano()))
	var keys []string
	db.exps.Scan(func(key string, _ interface{}) bool {
		if key > string(exkey) {
			return false
		}
		keys = append(keys, key[8:])
		return true
	})
	for _, key := range keys {
		db.del(key)
	}
}

func (db *database) set(o *object) (replaced bool) {
	v, replaced := db.keys.Set(o.key, o)
	if replaced {
		prev := v.(*object)
		if prev.expires > 0 {
			_, deleted := db.exps.Delete(prev.exkey())
			if !deleted {
				panic("expire entry missing")
			}
		}
	}
	if o.expires > 0 {
		db.exps.Set(o.exkey(), nil)
	}
	return replaced
}

func (db *database) del(key string) (prev *object, deleted bool) {
	v, deleted := db.keys.Delete(key)
	if deleted {
		prev := v.(*object)
		if prev.expires > 0 {
			_, deleted := db.exps.Delete(prev.exkey())
			if !deleted {
				panic("expire entry missing")
			}
		}
		return prev, true
	}
	return nil, false
}

func (db *database) get(key string) (*object, bool) {
	v, ok := db.keys.Get(key)
	if ok {
		return v.(*object), true
	}
	return nil, false
}

// SET key value [EX seconds]
func cmdSET(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) < 3 {
		return nil, uhaha.ErrWrongNumArgs
	}
	var ex float64
	for i := 3; i < len(args); i++ {
		switch strings.ToLower(string(args[i])) {
		case "ex":
			i++
			if i == len(args) {
				return nil, uhaha.ErrSyntax
			}
			var err error
			ex, err = strconv.ParseFloat(string(args[i]), 64)
			if err != nil || ex <= 0 {
				return nil, uhaha.ErrSyntax
			}
		default:
			return nil, uhaha.ErrSyntax
		}
	}
	o := new(object)
	o.created = m.Now().UnixNano()
	o.key = string(args[1])
	o.value = string(args[2])
	if ex == 0 && strings.HasPrefix(o.key, "key:") && o.value == "xxx" {
		r := m.Rand().Int()
		if r%2 == 0 {
			// add a random expires between 0 and 10 seconds
			ex = (float64(r%1000000) / 100000)
		}
	}
	if ex > 0 {
		o.expires = o.created + int64(ex*1e9)
	}
	db.set(o)
	return redcon.SimpleString("OK"), nil
}

var quoteURLS = []string{
	"https://gist.githubusercontent.com/tidwall/22140b7dd4e13f284c8c2663287178a0/raw/4d707b74de37f33da59b0b2e01c4d322e99dbc18/quote1.txt",
	"https://gist.githubusercontent.com/tidwall/ed62f7d7f429163af09c4904d42db20c/raw/00edea8e6988184050ab9bc6e43e743da070a19a/quote2.txt",
	"https://gist.githubusercontent.com/tidwall/ecc422812fcd50bfb571705de5f115eb/raw/9f0194a508154f80747bc860beba1d81e214664f/quote3.txt",
	"https://gist.githubusercontent.com/tidwall/231779f2a54e5fa956849c9376c78f6b/raw/44fa40e2f0cd81401f97b918ebe8f24d7eb59272/quote4.txt",
}

// SETRANDQUOTE key
// help: sets the key to a random quote
func cmdSETRANDQUOTE(m uhaha.Machine, args []string) (interface{}, error) {
	// This is running as a intermediate command, and we intend to translate the
	// arguments using the FilterArgs() type.
	//
	// Here we use the standard math/rand package instead of Machine.Rand and
	// download a random quote using the Go http client.
	if len(args) != 2 {
		return nil, uhaha.ErrWrongNumArgs
	}

	// Generate the random url.
	url := quoteURLS[rand.Int()%len(quoteURLS)]

	// Download the quote
	resp, err := http.DefaultClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	value, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status: %d\n%s", resp.StatusCode, value)
	}

	// Filter the args to make a new SET command that includes the quote
	args = []string{"SET", args[1], string(value)}

	return uhaha.FilterArgs(args), nil
}

func (o *object) exkey() string {
	b := make([]byte, 8+len(o.key))
	binary.BigEndian.PutUint64(b, uint64(o.expires))
	copy(b[8:], o.key)
	return string(b)
}

// DBSIZE
func cmdDBSIZE(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) != 1 {
		return nil, uhaha.ErrWrongNumArgs
	}
	return redcon.SimpleInt(db.keys.Len()), nil
}

// GET key
func cmdGET(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) != 2 {
		return nil, uhaha.ErrWrongNumArgs
	}
	o, ok := db.get(string(args[1]))
	if ok {
		return o.value, nil
	}
	return nil, nil
}

// KEYS pattern
// help: return a list of all keys matching the provided pattern
func cmdKEYS(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) != 2 {
		return nil, uhaha.ErrWrongNumArgs
	}
	pattern := string(args[1])
	var keys []string
	min, max := match.Allowable(pattern)
	if min == "" && max == "" {
		db.keys.Scan(func(key string, _ interface{}) bool {
			if match.Match(key, pattern) {
				keys = append(keys, key)
			}
			return true
		})
	} else {
		db.keys.Ascend(min, func(key string, _ interface{}) bool {
			if key > max {
				return false
			}
			if match.Match(key, pattern) {
				keys = append(keys, key)
			}
			return true
		})
	}
	return keys, nil
}

// DEL key [key ...]
// help: delete one or more keys. Returns the number of keys deleted.
func cmdDEL(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) < 2 {
		return nil, uhaha.ErrWrongNumArgs
	}
	var n int
	for i := 1; i < len(args); i++ {
		if _, deleted := db.del(string(args[i])); deleted {
			n++
		}
	}
	return redcon.SimpleInt(n), nil
}

func multiPOWER(s uhaha.Service) (interface{}, error) {
	resp, _, err := s.Send([]string{"hello", "world"}, nil).Recv()
	return resp, err
}

// MONITOR
// help: monitors all of the commands from all clients
func cmdMONITOR(m uhaha.Machine, args []string) (interface{}, error) {
	// Here we'll return a Hijack type that is just a function that will
	// take over the client connection in an isolated context.
	return uhaha.Hijack(hijackedMONITOR), nil
}

func hijackedMONITOR(s uhaha.Service, conn uhaha.HijackedConn) {
	obs := s.Monitor().NewObserver()
	s.Log().Printf("hijack opened: %s", conn.RemoteAddr())
	defer func() {
		s.Log().Printf("hijack closed: %s", conn.RemoteAddr())
		obs.Stop()
		conn.Close()
	}()
	conn.WriteAny(redcon.SimpleString("OK"))
	conn.Flush()
	go func() {
		defer obs.Stop()
		for {
			// Wait for any incoming command or error and immediately kill
			// the connection, which will in turn stop the observer.
			if _, err := conn.ReadCommand(); err != nil {
				return
			}
		}
	}()
	// Range over the observer's messages and send to the hijacked client.
	for msg := range obs.C() {
		var args string
		for i := 0; i < len(msg.Args); i++ {
			args += " " + strconv.Quote(msg.Args[i])
		}
		conn.WriteAny(redcon.SimpleString(fmt.Sprintf("%0.6f [0 %s]%s",
			float64(time.Now().UnixNano())/1e9, msg.Addr, args,
		)))
		conn.Flush()
	}
}

// #region -- SNAPSHOT & RESTORE

type dbSnapshot struct {
	objs []*object
}

func snapWriteObject(w *sds.Writer, o *object) error {
	if err := w.WriteString(o.key); err != nil {
		return err
	}
	if err := w.WriteString(o.value); err != nil {
		return err
	}
	if err := w.WriteInt64(o.created); err != nil {
		return err
	}
	if err := w.WriteInt64(o.expires); err != nil {
		return err
	}
	return nil
}

func (s *dbSnapshot) Persist(wr io.Writer) error {
	w := sds.NewWriter(wr)
	if err := w.WriteUvarint(uint64(len(s.objs))); err != nil {
		return err
	}
	for _, o := range s.objs {
		if err := snapWriteObject(w, o); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (s *dbSnapshot) Done(path string) {
	if path != "" {
		// snapshot was a success.
	}
}

func snapshot(data interface{}) (uhaha.Snapshot, error) {
	db := data.(*database)
	snap := new(dbSnapshot)
	snap.objs = make([]*object, 0, db.keys.Len())
	db.keys.Scan(func(_ string, v interface{}) bool {
		snap.objs = append(snap.objs, v.(*object))
		return true
	})
	return snap, nil
}

func snapReadObject(r *sds.Reader) (*object, error) {
	o := new(object)
	var err error
	o.key, err = r.ReadString()
	if err != nil {
		return nil, err
	}
	o.value, err = r.ReadString()
	if err != nil {
		return nil, err
	}
	o.created, err = r.ReadInt64()
	if err != nil {
		return nil, err
	}
	o.expires, err = r.ReadInt64()
	if err != nil {
		return nil, err
	}
	return o, nil
}

func restore(rd io.Reader) (interface{}, error) {
	db := new(database)
	r := sds.NewReader(rd)
	n, err := r.ReadUvarint()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < n; i++ {
		o, err := snapReadObject(r)
		if err != nil {
			return nil, err
		}
		db.set(o)
	}
	return db, nil
}

// #endregion -- SNAPSHOT & RESTORE
