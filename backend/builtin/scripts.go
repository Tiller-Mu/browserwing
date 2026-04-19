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
		existing, err := db.GetScript(s.ID)
		if err == nil && existing != nil {
			// Always update builtin scripts so fixes take effect on restart
			s.CreatedAt = existing.CreatedAt
			s.UpdatedAt = time.Now()
			if err := db.SaveScript(&s); err != nil {
				log.Printf("Warning: failed to update builtin script %q: %v", s.Name, err)
			}
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
	kr36Hot(),
	v2exHot(),
	githubTrending(),
	tiebaHot(),
	doubanTop250(),
	redditPopular(),
	productHuntHot(),
	stackOverflowHot(),
	hupuHot(),
	linuxDoHot(),
	eastmoneyHotRank(),
	xueqiuHot(),
	imdbTrending(),
	douyinHot(),
	sinaFinanceRank(),
}

func bilibiliHot() models.Script {
	return models.Script{
		ID:          "builtin-bilibili-hot",
		Name:        "bilibili-hot",
		Description: "获取 B 站热门视频排行榜",
		URL:         "https://www.bilibili.com/v/popular/rank/all",
		Tags:        []string{"builtin", "bilibili", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "bilibili_hot",
		MCPCommandDescription: "获取 B 站热门视频排行榜",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://www.bilibili.com/v/popular/rank/all",
			},
			{
				Type:     "sleep",
				Duration: 3000,
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.rank-item, .rank-list .item, li.rank-item');
if (!items.length) items = document.querySelectorAll('[class*="rank-item"], [class*="video-card"]');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.title, .info a, [class*="title"]');
  var linkEl = el.querySelector('a[href*="/video/"]') || (titleEl && titleEl.closest('a'));
  var playEl = el.querySelector('[class*="play"], [class*="view"], .detail-state .data-box');
  var authorEl = el.querySelector('[class*="author"], [class*="up-name"], .detail-state .data-box:nth-child(2)');
  if (titleEl) {
    var href = linkEl ? linkEl.href : '';
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      url: href,
      play: playEl ? playEl.textContent.trim() : '',
      author: authorEl ? authorEl.textContent.trim() : ''
    });
  }
});
return JSON.stringify(list);
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
		URL:         "https://weibo.com",
		Tags:        []string{"builtin", "weibo", "热搜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "weibo_hot",
		MCPCommandDescription: "获取微博热搜榜",
		Actions: []models.ScriptAction{
			{
				Type: "navigate",
				URL:  "https://weibo.com",
			},
			{
				Type:     "sleep",
				Duration: 3000,
			},
			{
				Type:         "evaluate",
				VariableName: "hot_list",
				JSCode: `
var resp = await fetch('https://weibo.com/ajax/side/hotSearch');
if (!resp.ok) {
  return JSON.stringify([]);
}
var json = await resp.json();
var realtime = (json.data && json.data.realtime) || [];
return JSON.stringify(realtime.slice(0, 30).map(function(v, i) {
  return {
    rank: i + 1,
    title: v.note || v.word,
    heat: v.num,
    category: v.category || '',
    url: 'https://s.weibo.com/weibo?q=' + encodeURIComponent('#' + (v.note || v.word) + '#')
  };
}));
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

func douyinHot() models.Script {
	return models.Script{
		ID:          "builtin-douyin-hot",
		Name:        "douyin-hot",
		Description: "获取抖音热搜榜",
		URL:         "https://www.douyin.com/hot",
		Tags:        []string{"builtin", "douyin", "热搜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "douyin_hot",
		MCPCommandDescription: "获取抖音热搜榜",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.douyin.com/hot"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('[class*="hot-list"] li, [class*="HotBoardList"] li, .trending-item');
if (!items.length) items = document.querySelectorAll('ul li a[href*="/hot/"]');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('[class*="title"], span, a');
  var hotEl = el.querySelector('[class*="hot"], [class*="count"]');
  if (titleEl && titleEl.textContent.trim()) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      heat: hotEl ? hotEl.textContent.trim() : ''
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

func kr36Hot() models.Script {
	return models.Script{
		ID:          "builtin-36kr-hot",
		Name:        "36kr-hot",
		Description: "获取 36 氪热榜文章",
		URL:         "https://www.36kr.com/hot-list/catalog",
		Tags:        []string{"builtin", "36kr", "科技", "热榜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "kr36_hot",
		MCPCommandDescription: "获取 36 氪热榜文章",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.36kr.com/hot-list/catalog"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var links = document.querySelectorAll('a[href*="/p/"]');
var seen = {};
var list = [];
links.forEach(function(a) {
  var title = a.textContent.trim();
  var href = a.getAttribute('href');
  if (!title || !href || seen[href]) return;
  seen[href] = true;
  var url = href.startsWith('http') ? href : 'https://36kr.com' + href;
  list.push({ rank: list.length + 1, title: title, url: url });
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func v2exHot() models.Script {
	return models.Script{
		ID:          "builtin-v2ex-hot",
		Name:        "v2ex-hot",
		Description: "获取 V2EX 热门主题",
		URL:         "https://www.v2ex.com/api/topics/hot.json",
		Tags:        []string{"builtin", "v2ex", "技术", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "v2ex_hot",
		MCPCommandDescription: "获取 V2EX 热门主题",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "about:blank"},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('https://www.v2ex.com/api/topics/hot.json');
const data = await resp.json();
return JSON.stringify(data.map((t, i) => ({
  rank: i + 1,
  title: t.title,
  node: t.node ? t.node.title : '',
  replies: t.replies,
  url: t.url,
})));
`,
			},
		},
	}
}

func githubTrending() models.Script {
	return models.Script{
		ID:          "builtin-github-trending",
		Name:        "github-trending",
		Description: "获取 GitHub Trending 仓库",
		URL:         "https://github.com/trending",
		Tags:        []string{"builtin", "github", "开源", "trending"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "github_trending",
		MCPCommandDescription: "获取 GitHub Trending 仓库",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://github.com/trending"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var rows = document.querySelectorAll('article.Box-row, [class*="Box-row"]');
var list = [];
rows.forEach(function(row, i) {
  var repoLink = row.querySelector('h2 a, h1 a');
  var desc = row.querySelector('p');
  var lang = row.querySelector('[itemprop="programmingLanguage"], span[class*="repo-language-color"] + span');
  var stars = row.querySelector('a[href*="/stargazers"], span.d-inline-block');
  if (repoLink) {
    var href = repoLink.getAttribute('href');
    list.push({
      rank: i + 1,
      repo: href.replace(/^\//, ''),
      description: desc ? desc.textContent.trim() : '',
      language: lang ? lang.textContent.trim() : '',
      stars: stars ? stars.textContent.trim() : '',
      url: 'https://github.com' + href,
    });
  }
});
return JSON.stringify(list);
`,
			},
		},
	}
}

func tiebaHot() models.Script {
	return models.Script{
		ID:          "builtin-tieba-hot",
		Name:        "tieba-hot",
		Description: "获取百度贴吧热议话题",
		URL:         "https://tieba.baidu.com/hottopic/browse/topicList",
		Tags:        []string{"builtin", "tieba", "贴吧", "热议"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "tieba_hot",
		MCPCommandDescription: "获取百度贴吧热议话题",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://tieba.baidu.com/hottopic/browse/topicList?res_type=1"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('li.topic-top-item, [class*="topic-item"], .topic-list li');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('a.topic-text, [class*="topic-text"], a');
  var numEl = el.querySelector('span.topic-num, [class*="topic-num"]');
  var descEl = el.querySelector('p.topic-top-item-desc, [class*="desc"]');
  if (titleEl && titleEl.textContent.trim()) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      discussions: numEl ? numEl.textContent.trim() : '',
      description: descEl ? descEl.textContent.trim() : '',
      url: titleEl.href || '',
    });
  }
});
return JSON.stringify(list);
`,
			},
		},
	}
}

func doubanTop250() models.Script {
	return models.Script{
		ID:          "builtin-douban-top250",
		Name:        "douban-top250",
		Description: "获取豆瓣电影 Top 250",
		URL:         "https://movie.douban.com/top250",
		Tags:        []string{"builtin", "douban", "电影", "Top250"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "douban_top250",
		MCPCommandDescription: "获取豆瓣电影 Top 250（前25部）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://movie.douban.com/top250"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.item, ol.grid_view li');
var list = [];
items.forEach(function(el) {
  var rankEl = el.querySelector('.pic em, em');
  var titleEl = el.querySelector('.title, .hd a span:first-child');
  var ratingEl = el.querySelector('.rating_num, [class*="rating_num"]');
  var linkEl = el.querySelector('a[href*="subject"]');
  if (titleEl) {
    list.push({
      rank: rankEl ? parseInt(rankEl.textContent) : list.length + 1,
      title: titleEl.textContent.trim(),
      rating: ratingEl ? ratingEl.textContent.trim() : '',
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

func redditPopular() models.Script {
	return models.Script{
		ID:          "builtin-reddit-popular",
		Name:        "reddit-popular",
		Description: "获取 Reddit 热门帖子",
		URL:         "https://www.reddit.com/r/popular/",
		Tags:        []string{"builtin", "reddit", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "reddit_popular",
		MCPCommandDescription: "获取 Reddit 热门帖子",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.reddit.com"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('/r/popular.json?limit=25&raw_json=1');
const data = await resp.json();
const posts = data.data.children;
return JSON.stringify(posts.map((p, i) => ({
  rank: i + 1,
  title: p.data.title,
  subreddit: p.data.subreddit,
  score: p.data.score,
  comments: p.data.num_comments,
  url: 'https://www.reddit.com' + p.data.permalink,
})));
`,
			},
		},
	}
}

func productHuntHot() models.Script {
	return models.Script{
		ID:          "builtin-producthunt-hot",
		Name:        "producthunt-hot",
		Description: "获取 Product Hunt 今日热门产品",
		URL:         "https://www.producthunt.com",
		Tags:        []string{"builtin", "producthunt", "产品", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "producthunt_hot",
		MCPCommandDescription: "获取 Product Hunt 今日热门产品",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.producthunt.com"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var links = document.querySelectorAll('a[href^="/posts/"]');
var seen = {};
var list = [];
links.forEach(function(a) {
  var href = a.getAttribute('href');
  if (!href || href.includes('/reviews') || seen[href]) return;
  seen[href] = true;
  var name = a.textContent.trim();
  if (!name || name.length > 100) return;
  list.push({
    rank: list.length + 1,
    name: name,
    url: 'https://www.producthunt.com' + href,
  });
});
return JSON.stringify(list.slice(0, 20));
`,
			},
		},
	}
}

func stackOverflowHot() models.Script {
	return models.Script{
		ID:          "builtin-stackoverflow-hot",
		Name:        "stackoverflow-hot",
		Description: "获取 Stack Overflow 热门问题",
		URL:         "https://api.stackexchange.com/2.3/questions?order=desc&sort=hot&site=stackoverflow",
		Tags:        []string{"builtin", "stackoverflow", "编程", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "stackoverflow_hot",
		MCPCommandDescription: "获取 Stack Overflow 热门问题",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "about:blank"},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('https://api.stackexchange.com/2.3/questions?order=desc&sort=hot&site=stackoverflow&pagesize=25');
const data = await resp.json();
return JSON.stringify(data.items.map((q, i) => ({
  rank: i + 1,
  title: q.title,
  score: q.score,
  answers: q.answer_count,
  tags: q.tags.slice(0, 3).join(', '),
  url: q.link,
})));
`,
			},
		},
	}
}

func hupuHot() models.Script {
	return models.Script{
		ID:          "builtin-hupu-hot",
		Name:        "hupu-hot",
		Description: "获取虎扑热帖",
		URL:         "https://bbs.hupu.com",
		Tags:        []string{"builtin", "hupu", "体育", "热帖"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "hupu_hot",
		MCPCommandDescription: "获取虎扑热帖",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://bbs.hupu.com"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var html = document.documentElement.outerHTML;
var regex = /<a[^>]*href="\/(\d{7,})\.html"[^>]*>(?:<[^>]*>)*([^<]+)/g;
var list = [];
var match;
var seen = {};
while ((match = regex.exec(html)) !== null) {
  var tid = match[1];
  var title = match[2].trim();
  if (!title || seen[tid]) continue;
  seen[tid] = true;
  list.push({
    rank: list.length + 1,
    title: title,
    url: 'https://bbs.hupu.com/' + tid + '.html',
  });
}
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func linuxDoHot() models.Script {
	return models.Script{
		ID:          "builtin-linux-do-hot",
		Name:        "linux-do-hot",
		Description: "获取 Linux.do 热门话题",
		URL:         "https://linux.do",
		Tags:        []string{"builtin", "linux-do", "技术", "社区"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "linux_do_hot",
		MCPCommandDescription: "获取 Linux.do 热门话题",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://linux.do"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('/top.json?per_page=25&period=weekly');
const data = await resp.json();
const topics = data.topic_list.topics;
return JSON.stringify(topics.map((t, i) => ({
  rank: i + 1,
  title: t.title,
  replies: t.posts_count - 1,
  likes: t.like_count,
  views: t.views,
  url: 'https://linux.do/t/topic/' + t.id,
})));
`,
			},
		},
	}
}

func eastmoneyHotRank() models.Script {
	return models.Script{
		ID:          "builtin-eastmoney-hot",
		Name:        "eastmoney-hot",
		Description: "获取东方财富人气股票排行",
		URL:         "https://guba.eastmoney.com/rank/",
		Tags:        []string{"builtin", "eastmoney", "股票", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "eastmoney_hot",
		MCPCommandDescription: "获取东方财富人气股票排行",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://guba.eastmoney.com/rank/"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var rows = document.querySelectorAll('table.rank_table tbody tr, #rankCont tr, [class*="rank"] table tr');
var list = [];
rows.forEach(function(tr, i) {
  var tds = tr.querySelectorAll('td');
  if (tds.length < 4) return;
  var codeEl = tr.querySelector('a.stock_code, a[href*="list,"]');
  var nameEl = tr.querySelector('td.nametd a[title], td:nth-child(2) a');
  var fansEl = tr.querySelector('td.fans, td:last-child');
  if (nameEl) {
    var code = codeEl ? codeEl.textContent.trim() : '';
    list.push({
      rank: list.length + 1,
      symbol: code,
      name: nameEl.getAttribute('title') || nameEl.textContent.trim(),
      heat: fansEl ? fansEl.textContent.trim() : '',
      url: 'https://guba.eastmoney.com/list,' + code + '.html',
    });
  }
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func xueqiuHot() models.Script {
	return models.Script{
		ID:          "builtin-xueqiu-hot",
		Name:        "xueqiu-hot",
		Description: "获取雪球热帖",
		URL:         "https://xueqiu.com",
		Tags:        []string{"builtin", "xueqiu", "金融", "热帖"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "xueqiu_hot",
		MCPCommandDescription: "获取雪球热帖",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://xueqiu.com"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('/statuses/hot/listV3.json?source=hot&page=1', {credentials: 'include'});
const data = await resp.json();
const items = data.items || [];
return JSON.stringify(items.map((item, i) => {
  var s = item.original_status || item;
  return {
    rank: i + 1,
    author: s.user ? s.user.screen_name : '',
    title: (s.description || s.text || '').replace(/<[^>]+>/g, '').slice(0, 80),
    likes: s.fav_count || 0,
    url: 'https://xueqiu.com' + (s.target || ''),
  };
}));
`,
			},
		},
	}
}

func imdbTrending() models.Script {
	return models.Script{
		ID:          "builtin-imdb-trending",
		Name:        "imdb-trending",
		Description: "获取 IMDB 热门电影",
		URL:         "https://www.imdb.com/chart/moviemeter/",
		Tags:        []string{"builtin", "imdb", "电影", "trending"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "imdb_trending",
		MCPCommandDescription: "获取 IMDB 热门电影",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.imdb.com/chart/moviemeter/"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var scripts = document.querySelectorAll('script[type="application/ld+json"]');
for (var s of scripts) {
  try {
    var json = JSON.parse(s.textContent);
    if (json['@type'] === 'ItemList' && json.itemListElement) {
      var list = json.itemListElement.slice(0, 25).map(function(el, i) {
        var item = el.item || el;
        return {
          rank: el.position || i + 1,
          title: item.name || '',
          rating: item.aggregateRating ? item.aggregateRating.ratingValue : '',
          genre: Array.isArray(item.genre) ? item.genre.join(', ') : (item.genre || ''),
          url: item.url ? (item.url.startsWith('http') ? item.url : 'https://www.imdb.com' + item.url) : '',
        };
      });
      return JSON.stringify(list);
    }
  } catch(e) {}
}
var rows = document.querySelectorAll('.ipc-metadata-list-summary-item, li[class*="ipc-metadata-list"]');
var fallback = [];
rows.forEach(function(row, i) {
  var titleEl = row.querySelector('h3, [class*="title"]');
  var ratingEl = row.querySelector('[class*="rating"], [aria-label*="rating"]');
  if (titleEl && i < 25) {
    fallback.push({
      rank: i + 1,
      title: titleEl.textContent.trim().replace(/^\d+\.\s*/, ''),
      rating: ratingEl ? ratingEl.textContent.trim() : '',
      url: '',
    });
  }
});
return JSON.stringify(fallback);
`,
			},
		},
	}
}

func sinaFinanceRank() models.Script {
	return models.Script{
		ID:          "builtin-sinafinance-rank",
		Name:        "sinafinance-rank",
		Description: "获取新浪财经涨幅排行榜",
		URL:         "https://finance.sina.com.cn/stock/",
		Tags:        []string{"builtin", "sinafinance", "股票", "涨幅榜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "sinafinance_rank",
		MCPCommandDescription: "获取新浪财经股票涨幅榜",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "about:blank"},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
const resp = await fetch('https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQNodeData?page=1&num=20&sort=changepercent&asc=0&node=hs_a&symbol=&_s_r_a=auto');
const data = await resp.json();
return JSON.stringify(data.map((s, i) => ({
  rank: i + 1,
  symbol: s.symbol,
  name: s.name,
  price: s.trade,
  change_percent: s.changepercent + '%',
  volume: s.volume,
})));
`,
			},
		},
	}
}
