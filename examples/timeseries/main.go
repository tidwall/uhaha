// Copyright 2020 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/btree"
	"github.com/tidwall/redcon"
	"github.com/tidwall/sds"
	"github.com/tidwall/uhaha"
)

const defaultRetain = time.Hour * 24 * 7

type database struct {
	totalSets    uint64
	totalDels    uint64
	retain       time.Duration
	measurements btree.Map[string, interface{}]
}

func main() {
	var conf uhaha.Config
	conf.Name = "timeseries"
	conf.Version = "0.0.1"
	conf.InitialData = &database{retain: defaultRetain}
	conf.Snapshot = snapshot
	conf.Restore = restore
	conf.Tick = tick
	conf.AddWriteCommand("write", cmdWRITE)
	conf.AddWriteCommand("retain", cmdRETAIN)
	conf.AddReadCommand("query", cmdQUERY)
	conf.AddReadCommand("stats", cmdSTATS)
	uhaha.Main(conf)
}

func parseTimestamp(now time.Time, s string) (uint64, error) {
	if s == "now" {
		return uint64(now.UnixNano()), nil
	}
	ts, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return ts, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return uint64(time.Duration(now.UnixNano()) + dur), nil
}

func tick(m uhaha.Machine) {
	db := m.Data().(*database)
	base := fmt.Sprintf("%020d", uint64(m.Now().Add(-db.retain).UnixNano()))
	var delPoints []string
	var delMeasurements []string
	db.measurements.Scan(func(measurement string, v interface{}) bool {
		mdb := v.(*btree.Map[string, interface{}])
		delPoints = delPoints[:0]
		mdb.Scan(func(point string, v interface{}) bool {
			if point > string(base) {
				return false
			}
			delPoints = append(delPoints, point)
			return true
		})
		for _, point := range delPoints {
			db.totalDels++
			mdb.Delete(point)
		}
		if mdb.Len() == 0 {
			delMeasurements = append(delMeasurements, measurement)
		}
		return true
	})
	for _, measurement := range delMeasurements {
		db.measurements.Delete(measurement)
	}
}

func (db *database) getMDB(measurment string, create bool) *btree.Map[string, interface{}] {
	v, _ := db.measurements.Get(measurment)
	if v != nil {
		return v.(*btree.Map[string, interface{}])
	}
	if !create {
		return nil
	}
	mdb := new(btree.Map[string, interface{}])
	db.measurements.Set(measurment, mdb)
	return mdb
}

// RETAIN [duration]
func cmdRETAIN(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	switch len(args) {
	case 1:
		return db.retain.String(), nil
	case 2:
		retain, err := time.ParseDuration(args[1])
		if err != nil || retain < 0 {
			return nil, uhaha.ErrSyntax
		}
		db.retain = retain
		return redcon.SimpleString("OK"), nil
	default:
		return nil, uhaha.ErrWrongNumArgs
	}
}

// WRITE measurement timestamp fields
func cmdWRITE(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	if len(args) != 4 {
		return nil, uhaha.ErrWrongNumArgs
	}
	if strings.IndexByte(args[1], ' ') != -1 ||
		strings.IndexByte(args[3], ' ') != -1 {
		return nil, uhaha.ErrSyntax
	}
	timestamp, err := parseTimestamp(m.Now(), args[2])
	if err != nil {
		return nil, err
	}
	mdb := db.getMDB(args[1], true)
	point := fmt.Sprintf("%020d %s", timestamp, args[3])
	if _, replaced := mdb.Set(point, nil); !replaced {
		db.totalSets++
	}
	return redcon.SimpleString("OK"), nil
}

// STATS
func cmdSTATS(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	npoints := 0
	db.measurements.Scan(func(measurement string, v interface{}) bool {
		mdb := v.(*btree.Map[string, interface{}])
		npoints += mdb.Len()
		return true
	})
	return map[string]string{
		"num_points":       fmt.Sprintf("%d", npoints),
		"num_measurements": fmt.Sprintf("%d", db.measurements.Len()),
		"retain":           fmt.Sprintf("%s", db.retain),
		"total_sets":       fmt.Sprintf("%d", db.totalSets),
		"total_dels":       fmt.Sprintf("%d", db.totalDels),
	}, nil
}

// QUERY measurement start end limit
// example: QUERY cpu -5m now
// example: QUERY cpu -10m -5m 1000
func cmdQUERY(m uhaha.Machine, args []string) (interface{}, error) {
	db := m.Data().(*database)
	now := m.Now()
	if len(args) != 5 {
		return nil, uhaha.ErrWrongNumArgs
	}
	start, err := parseTimestamp(now, args[2])
	if err != nil {
		return nil, err
	}
	end, err := parseTimestamp(now, args[3])
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(args[1]); i++ {
		if args[1][i] <= ' ' {
			return nil, uhaha.ErrSyntax
		}
	}
	var limit uint64
	if args[4] == "all" {
		limit = math.MaxUint64
	} else {
		limit, err = strconv.ParseUint(args[4], 10, 64)
		if err != nil {
			return nil, err
		}
	}
	mdb := db.getMDB(args[1], false)
	if mdb == nil {
		return []string{}, nil
	}
	pivot := fmt.Sprintf("%020d", start)
	final := fmt.Sprintf("%020d", end)
	var points []string
	var count uint64
	mdb.Ascend(pivot, func(point string, _ interface{}) bool {
		if count == limit || point > final {
			return false
		}
		points = append(points, args[1]+" "+strings.TrimLeft(point, "0"))
		count++
		return true
	})
	return points, nil
}

// #region SNAPSHOT & RESTORE

type snapPoint struct {
	measurement string
	point       string
}

type dbSnapshot struct {
	totalSets uint64
	totalDels uint64
	retain    time.Duration
	points    []snapPoint
}

func (s *dbSnapshot) Persist(wr io.Writer) error {
	w := sds.NewWriter(wr)
	if err := w.WriteUvarint(s.totalSets); err != nil {
		return err
	}
	if err := w.WriteUvarint(s.totalDels); err != nil {
		return err
	}
	if err := w.WriteVarint(int64(s.retain)); err != nil {
		return err
	}
	if err := w.WriteUvarint(uint64(len(s.points))); err != nil {
		return err
	}
	for _, point := range s.points {
		if err := w.WriteString(point.measurement); err != nil {
			return err
		}
		if err := w.WriteString(point.point); err != nil {
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
	snap.totalSets = db.totalSets
	snap.totalDels = db.totalDels
	snap.retain = db.retain
	db.measurements.Scan(func(measurement string, v interface{}) bool {
		mdb := v.(*btree.Map[string, interface{}])
		mdb.Scan(func(point string, _ interface{}) bool {
			snap.points = append(snap.points, snapPoint{measurement, point})
			return true
		})
		return true
	})
	return snap, nil
}

func restore(rd io.Reader) (interface{}, error) {
	db := new(database)
	r := sds.NewReader(rd)
	var err error
	if db.totalSets, err = r.ReadUvarint(); err != nil {
		return nil, err
	}
	if db.totalDels, err = r.ReadUvarint(); err != nil {
		return nil, err
	}
	retain, err := r.ReadVarint()
	if err != nil {
		return nil, err
	}
	db.retain = time.Duration(retain)
	n, err := r.ReadUvarint()
	if err != nil {
		return nil, err
	}
	var mdb *btree.Map[string, interface{}]
	var lastMeasurment string
	for i := uint64(0); i < n; i++ {
		measurement, err := r.ReadString()
		if err != nil {
			return nil, err
		}
		point, err := r.ReadString()
		if err != nil {
			return nil, err
		}
		if measurement != lastMeasurment {
			mdb = db.getMDB(measurement, true)
			lastMeasurment = measurement
		}
		mdb.Set(point, nil)
	}
	return db, nil
}

// #endregion -- SNAPSHOT & RESTORE
