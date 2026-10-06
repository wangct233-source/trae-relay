// Package usage 记录每次 API 消费（tokens / 积分 / 状态）并聚合每日统计。
package usage

import (
	"sort"
	"sync"
	"time"

	"trae-relay/internal/store"
)

// Record 一条消费记录。
type Record struct {
	ID         string   `json:"id"`
	Time       int64    `json:"time"` // unix 秒
	AccountID  string   `json:"account_id"`
	Model      string   `json:"model"`
	Input      float64  `json:"input_tokens"`
	Output     float64  `json:"output_tokens"`
	Cached     float64  `json:"cached_tokens"`
	Total      float64  `json:"total_tokens"`
	Credits    *float64 `json:"credits"` // nil = 未知，不伪装成 0
	Status     string   `json:"status"`  // ok / error
	Error      string   `json:"error,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// DayBucket 每日聚合。
type DayBucket struct {
	Requests  int64    `json:"requests"`
	Failed    int64    `json:"failed"`
	Input     float64  `json:"input_tokens"`
	Output    float64  `json:"output_tokens"`
	Cached    float64  `json:"cached_tokens"`
	Credits   *float64 `json:"credits"`
}

type data struct {
	Records []Record             `json:"records"`
	Stats   map[string]DayBucket `json:"stats"` // 日期(北京) -> 聚合
}

// Tracker 消费记录持久化（上限 500 条滚动）+ 每日统计。
type Tracker struct {
	file *store.JSONFile[data]
	mu   sync.Mutex
	max  int
}

const beijingOffset = 8 * 3600

func day(t time.Time) string {
	return t.UTC().Add(beijingOffset * time.Second).Format("2006-01-02")
}

// NewTracker 打开持久化文件。
func NewTracker(dataDir string) (*Tracker, error) {
	f, err := store.NewJSONFile[data](dataDir, "usage.json", data{
		Records: []Record{}, Stats: map[string]DayBucket{},
	})
	return &Tracker{file: f, max: 500}, err
}

// Start 记录一次请求开始（先落一条 error 占位，完成时更新）。
// 返回记录 ID 与提交函数。
func (t *Tracker) Start(accountID, model string) (string, func(Record)) {
	id := time.Now().Format("20060102-150405.000000000")
	t.mu.Lock()
	d := t.file.Get()
	start := time.Now()
	d.Records = append(d.Records, Record{
		ID: id, Time: start.Unix(), AccountID: accountID,
		Model: model, Status: "pending",
	})
	if len(d.Records) > t.max {
		d.Records = d.Records[len(d.Records)-t.max:]
	}
	_ = t.file.Update(func(cur *data) { cur.Records = d.Records })
	t.mu.Unlock()

	commit := func(r Record) {
		r.ID = id
		r.DurationMs = time.Since(start).Milliseconds()
		if r.Time == 0 {
			r.Time = start.Unix()
		}
		t.finish(id, r)
	}
	return id, commit
}

func (t *Tracker) finish(id string, r Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.file.Update(func(d *data) {
		for i := range d.Records {
			if d.Records[i].ID == id {
				d.Records[i] = r
				break
			}
		}
		key := day(time.Unix(r.Time, 0))
		b := d.Stats[key]
		b.Requests++
		if r.Status != "ok" {
			b.Failed++
		}
		b.Input += r.Input
		b.Output += r.Output
		b.Cached += r.Cached
		if r.Credits != nil {
			if b.Credits == nil {
				v := *r.Credits
				b.Credits = &v
			} else {
				*b.Credits += *r.Credits
			}
		}
		d.Stats[key] = b
	})
}

// Records 按时间倒序返回最近 limit 条记录。
func (t *Tracker) Records(limit int) []Record {
	all := t.file.Get().Records
	sort.Slice(all, func(i, j int) bool { return all[i].Time > all[j].Time })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

// Overview 返回最近 days 天的每日统计（按日期倒序）与汇总。
func (t *Tracker) Overview(days int) ([]struct {
	Day  string
	Data DayBucket
}, DayBucket) {
	stats := t.file.Get().Stats
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if days > 0 && len(keys) > days {
		keys = keys[:days]
	}
	out := make([]struct {
		Day  string
		Data DayBucket
	}, 0, len(keys))
	var total DayBucket
	for _, k := range keys {
		b := stats[k]
		out = append(out, struct {
			Day  string
			Data DayBucket
		}{k, b})
		total.Requests += b.Requests
		total.Failed += b.Failed
		total.Input += b.Input
		total.Output += b.Output
		total.Cached += b.Cached
		if b.Credits != nil {
			if total.Credits == nil {
				v := *b.Credits
				total.Credits = &v
			} else {
				*total.Credits += *b.Credits
			}
		}
	}
	return out, total
}
