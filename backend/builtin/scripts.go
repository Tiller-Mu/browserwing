// Package builtin provides pre-packaged scripts for popular platforms.
// These scripts are loaded into the database on first run and marked
// with the "builtin" tag so users can discover them immediately.
package builtin

import (
	"log"
	"time"

	"github.com/browserwing/browserwing/models"
)

type ScriptStore interface {
	GetScript(id string) (*models.Script, error)
	SaveScript(script *models.Script) error
}

func LoadBuiltinScripts(db ScriptStore) {
	for _, s := range builtinScripts {
		if existing, err := db.GetScript(s.ID); err == nil && existing != nil {
			continue
		}
		s.CreatedAt = time.Now()
		s.UpdatedAt = time.Now()
		if err := db.SaveScript(&s); err != nil {
			log.Printf("Warning: failed to load builtin script %q: %v", s.Name, err)
		} else {
			log.Printf("✓ Loaded builtin script: %s", s.Name)
		}
	}
}

var builtinScripts = []models.Script{
	bilibiliHot(),
	zhihuHot(),
	weiboHot(),
	doubanMovieTop(),
	hackerNewsTop(),
}

func bilibiliHot() models.Script {
	return models.Script{
		ID:          "builtin-bilibili-hot",
		Name:        "bilibili-hot",
		Description: "获取 B 站热门视频排行榜",
		URL:         "https://api.bilibili.com/x/web-interface/ranking/v2",
		Tags:        []string{"builtin", "bilibili", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "bilibili_hot",
		MCPCommandDescription: "获取 B 站热门视频排行榜",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://api.bilibili.com/x/web-interface/ranking/v2",
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
const resp = await fetch('https://api.bilibili.com/x/web-interface/ranking/v2');
const json = await resp.json();
const list = (json.data && json.data.list) || [];
return JSON.stringify(list.slice(0, 20).map((v, i) => ({
  rank: i + 1,
  title: v.title,
  author: v.owner && v.owner.name,
  play: v.stat && v.stat.view,
  like: v.stat && v.stat.like,
  url: 'https://www.bilibili.com/video/' + v.bvid,
})));
`,
			},
		},
	}
}

func zhihuHot() models.Script {
	return models.Script{
		ID:          "builtin-zhihu-hot",
		Name:        "zhihu-hot",
		Description: "获取知乎热榜 Top 50",
		URL:         "https://www.zhihu.com/hot",
		Tags:        []string{"builtin", "zhihu", "热榜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "zhihu_hot",
		MCPCommandDescription: "获取知乎热榜",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://www.zhihu.com/hot",
			},
			{
				Type:     "sleep",
				Duration: 3000,
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('[class*="HotItem"]');
if (!items.length) items = document.querySelectorAll('.HotList-item');
if (!items.length) items = document.querySelectorAll('section a[href*="/question/"]');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('[class*="HotItem-title"], .HotList-itemTitle, h2');
  var metricEl = el.querySelector('[class*="HotItem-metrics"], .HotList-itemMetrics');
  var linkEl = el.closest('a') || el.querySelector('a');
  if (titleEl) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      heat: metricEl ? metricEl.textContent.trim() : '',
      url: linkEl ? linkEl.href : '',
    });
  }
});
return JSON.stringify(list);
`,
			},
		},
	}
}

func weiboHot() models.Script {
	return models.Script{
		ID:          "builtin-weibo-hot",
		Name:        "weibo-hot",
		Description: "获取微博热搜榜",
		URL:         "https://weibo.com/ajax/side/hotSearch",
		Tags:        []string{"builtin", "weibo", "热搜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "weibo_hot",
		MCPCommandDescription: "获取微博热搜榜",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://weibo.com/ajax/side/hotSearch",
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
const resp = await fetch('https://weibo.com/ajax/side/hotSearch');
const json = await resp.json();
const realtime = (json.data && json.data.realtime) || [];
return JSON.stringify(realtime.slice(0, 30).map((v, i) => ({
  rank: i + 1,
  title: v.note || v.word,
  heat: v.num,
  category: v.category || '',
  url: 'https://s.weibo.com/weibo?q=' + encodeURIComponent('#' + (v.note || v.word) + '#'),
})));
`,
			},
		},
	}
}

func doubanMovieTop() models.Script {
	return models.Script{
		ID:          "builtin-douban-movie-hot",
		Name:        "douban-movie-hot",
		Description: "获取豆瓣热门电影",
		URL:         "https://movie.douban.com/chart",
		Tags:        []string{"builtin", "douban", "电影"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "douban_movie_hot",
		MCPCommandDescription: "获取豆瓣热门电影",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://movie.douban.com/chart",
			},
			{
				Type:     "sleep",
				Duration: 2000,
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
const items = document.querySelectorAll('.item');
const list = [];
items.forEach((el, i) => {
  const titleEl = el.querySelector('.title a, .pl2 a');
  const ratingEl = el.querySelector('.rating_nums');
  const imgEl = el.querySelector('img');
  if (titleEl) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim().replace(/\\s+/g, ' '),
      rating: ratingEl ? ratingEl.textContent.trim() : '',
      url: titleEl.href || '',
      cover: imgEl ? imgEl.src : '',
    });
  }
});
return JSON.stringify(list);
`,
			},
		},
	}
}

func hackerNewsTop() models.Script {
	return models.Script{
		ID:          "builtin-hackernews-top",
		Name:        "hackernews-top",
		Description: "获取 Hacker News 当前热门文章",
		URL:         "https://hacker-news.firebaseio.com/v0/topstories.json",
		Tags:        []string{"builtin", "hackernews", "tech"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "hackernews_top",
		MCPCommandDescription: "Get top stories from Hacker News",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "about:blank",
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
const idsResp = await fetch('https://hacker-news.firebaseio.com/v0/topstories.json');
const ids = await idsResp.json();
const top20 = ids.slice(0, 20);
const stories = await Promise.all(top20.map(async (id) => {
  const r = await fetch('https://hacker-news.firebaseio.com/v0/item/' + id + '.json');
  return r.json();
}));
return JSON.stringify(stories.map((s, i) => ({
  rank: i + 1,
  title: s.title,
  score: s.score,
  author: s.by,
  comments: s.descendants || 0,
  url: s.url || ('https://news.ycombinator.com/item?id=' + s.id),
})));
`,
			},
		},
	}
}
