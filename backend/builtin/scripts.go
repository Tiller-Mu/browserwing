// Package builtin provides pre-packaged scripts for popular platforms.
// These scripts are loaded into the database on first run and marked
// with the "builtin" tag so users can discover them immediately.
//
// Script definitions are fetched from remote JSON (GitHub → Gitee fallback)
// on startup. If remote fetch fails, hardcoded definitions are used as fallback.
package builtin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/browserwing/browserwing/models"
)

const (
	githubBaseURL = "https://raw.githubusercontent.com/browserwing/browserwing/main/builtin-scripts"
	giteeBaseURL  = "https://gitee.com/browserwing/browserwing/raw/main/builtin-scripts"

	// Legacy single-file fallback
	githubLegacyURL = "https://raw.githubusercontent.com/browserwing/browserwing/main/builtin-scripts.json"
	giteeLegacyURL  = "https://gitee.com/browserwing/browserwing/raw/main/builtin-scripts.json"
)

type ScriptStore interface {
	GetScript(id string) (*models.Script, error)
	SaveScript(script *models.Script) error
}

// GetBuiltinScripts returns all hardcoded builtin script definitions.
func GetBuiltinScripts() []models.Script {
	return builtinScripts
}

func LoadBuiltinScripts(db ScriptStore) {
	scripts := fetchRemoteScripts()
	source := "remote"
	if scripts == nil {
		log.Printf("[builtin] using %d local builtin scripts", len(builtinScripts))
		scripts = builtinScripts
		source = "local"
	} else {
		log.Printf("Fetched %d builtin scripts from remote", len(scripts))
	}

	var added, updated, skipped int
	for _, s := range scripts {
		if !strings.HasPrefix(s.ID, "builtin-") {
			continue
		}

		existing, err := db.GetScript(s.ID)
		if err != nil || existing == nil {
			s.CreatedAt = time.Now()
			s.UpdatedAt = time.Now()
			if err := db.SaveScript(&s); err != nil {
				log.Printf("Warning: failed to load builtin script %q: %v", s.Name, err)
			} else {
				added++
			}
			continue
		}

		if scriptContentEqual(existing, &s) {
			skipped++
			continue
		}

		s.CreatedAt = existing.CreatedAt
		s.UpdatedAt = time.Now()
		if err := db.SaveScript(&s); err != nil {
			log.Printf("Warning: failed to update builtin script %q: %v", s.Name, err)
		} else {
			updated++
			log.Printf("↑ Updated builtin script: %s", s.Name)
		}
	}

	log.Printf("Builtin scripts sync complete (%s): %d added, %d updated, %d unchanged",
		source, added, updated, skipped)
}

func scriptContentEqual(a, b *models.Script) bool {
	hashA := scriptHash(a)
	hashB := scriptHash(b)
	return hashA == hashB
}

func scriptHash(s *models.Script) string {
	h := sha256.New()
	h.Write([]byte(s.Name))
	h.Write([]byte(s.Description))
	h.Write([]byte(s.URL))
	actionsJSON, _ := json.Marshal(s.Actions)
	h.Write(actionsJSON)
	tagsJSON, _ := json.Marshal(s.Tags)
	h.Write(tagsJSON)
	if s.RequiresLogin {
		h.Write([]byte("login"))
	}
	varsJSON, _ := json.Marshal(s.Variables)
	h.Write(varsJSON)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type scriptIndex struct {
	Files []string `json:"files"`
}

func fetchRemoteScripts() []models.Script {
	client := &http.Client{Timeout: 10 * time.Second}

	// Try index-based loading (split files, concurrent)
	for _, base := range []string{githubBaseURL, giteeBaseURL} {
		scripts, err := fetchFromIndex(client, base)
		if err != nil {
			log.Printf("[builtin] remote index unavailable (%s), trying next source...", base)
			continue
		}
		return scripts
	}

	// Fallback to legacy single-file
	for _, url := range []string{githubLegacyURL, giteeLegacyURL} {
		scripts, err := fetchFromURL(client, url)
		if err != nil {
			log.Printf("[builtin] legacy source unavailable (%s), trying next...", url)
			continue
		}
		return scripts
	}
	return nil
}

func fetchFromIndex(client *http.Client, baseURL string) ([]models.Script, error) {
	indexURL := baseURL + "/index.json"
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for index", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var idx scriptIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("invalid index JSON: %w", err)
	}
	if len(idx.Files) == 0 {
		return nil, fmt.Errorf("empty index")
	}

	type fetchResult struct {
		scripts []models.Script
		err     error
		file    string
	}

	ch := make(chan fetchResult, len(idx.Files))
	for _, file := range idx.Files {
		go func(f string) {
			fileURL := baseURL + "/" + f
			scripts, err := fetchFromURL(client, fileURL)
			ch <- fetchResult{scripts: scripts, err: err, file: f}
		}(file)
	}

	var all []models.Script
	for range idx.Files {
		result := <-ch
		if result.err != nil {
			log.Printf("Warning: failed to fetch %s: %v", result.file, result.err)
			continue
		}
		all = append(all, result.scripts...)
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no scripts loaded from index files")
	}

	log.Printf("Loaded %d scripts from %d category files", len(all), len(idx.Files))
	return all, nil
}

func fetchFromURL(client *http.Client, url string) ([]models.Script, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var scripts []models.Script
	if err := json.Unmarshal(body, &scripts); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if len(scripts) == 0 {
		return nil, fmt.Errorf("empty scripts list")
	}

	return scripts, nil
}

var builtinScripts = []models.Script{
	// 无需登录
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
	imdbTrending(),
	sinaFinanceRank(),
	// 需要登录
	douyinHot(),
	xueqiuHot(),
	xiaohongshuHot(),
	bossHot(),
	jdSearch(),
	taobaoSearch(),
	twitterTrending(),
	linkedinJobs(),
	smzdmHot(),
	weixinArticle(),
	jueJinHot(),
	toutiaoHot(),
	// --- Extra: Tech ---
	devtoTop(),
	lobstersHot(),
	hfTop(),
	arxivNew(),
	giteeHot(),
	nowcoderHot(),
	// --- Extra: News ---
	bbcNews(),
	googleNews(),
	// --- Extra: Entertainment ---
	steamTopSellers(),
	youtubetrending(),
	// --- Extra: Finance ---
	binanceTop(),
	// --- Extra: Social ---
	blueskyTrending(),
	jikeHot(),
	// --- Extra: Reading ---
	wereadRanking(),
	mediumTrending(),
	// --- Extra: Shopping ---
	amazonBestsellers(),
	xianyuHot(),
	// --- Extra: Jobs ---
	maimaiSearch(),
	// --- Extra: Academic ---
	wikipediaTrending(),
	// --- Extra: Misc ---
	lesswrongCurated(),
	keErshoufang(),
	doubanBookHot(),
	zhihuDaily(),
	// --- Extra: Search ---
	weiboSearch(),
	doubanSearch(),
	githubSearch(),
	zhihuSearch(),
	// --- Extra2: Finance ---
	thsHotRank(),
	tdxHotRank(),
	yahooFinanceQuote(),
	barchartOptions(),
	// --- Extra2: News ---
	bloombergNews(),
	reutersSearch(),
	sinablogHot(),
	// --- Extra2: Entertainment ---
	applePodcastsTop(),
	tiktokExplore(),
	pixivRanking(),
	// --- Extra2: Reading ---
	substackFeed(),
	dictionaryLookup(),
	// --- Extra2: Academic ---
	googleScholarSearch(),
	baiduScholarSearch(),
	wanfangSearch(),
	cnkiSearch(),
	// --- Extra2: Other ---
	googleTrends(),
	govPolicy(),
	govLaw(),
	ctripSearch(),
	jianyuSearch(),
	// --- Publish ---
	xiaohongshuPublish(),
	twitterPost(),
	twitterReply(),
	// --- XHS Extra ---
	xiaohongshuSearch(),
	xiaohongshuNote(),
	xiaohongshuUser(),
	// --- Twitter Extra ---
	twitterSearch(),
	twitterProfile(),
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
		Description: "获取抖音热搜榜（需要登录抖音）",
		URL:         "https://www.douyin.com/hot",
		Tags:        []string{"builtin", "douyin", "热搜", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
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
		URL:         "https://www.v2ex.com",
		Tags:        []string{"builtin", "v2ex", "技术", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "v2ex_hot",
		MCPCommandDescription: "获取 V2EX 热门主题",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.v2ex.com"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
(async function() {
  try {
    var resp = await fetch('/api/topics/hot.json');
    var data = await resp.json();
    return data.map(function(t, i) {
      return {rank: i+1, title: t.title, node: t.node ? t.node.title : '', replies: t.replies, url: t.url};
    });
  } catch(e) {
    var items = document.querySelectorAll('.cell.item');
    var list = [];
    items.forEach(function(el, i) {
      var a = el.querySelector('.item_title a');
      var node = el.querySelector('.node');
      if (a) list.push({rank: i+1, title: a.textContent.trim(), node: node ? node.textContent.trim() : '', url: 'https://www.v2ex.com' + a.getAttribute('href')});
    });
    return list.slice(0, 30);
  }
})()
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
			{Type: "navigate", URL: "https://linux.do/top?period=weekly"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
(async function() {
  try {
    var resp = await fetch('/top.json?per_page=25&period=weekly');
    var data = await resp.json();
    var topics = data.topic_list.topics;
    return topics.map(function(t, i) {
      return {rank: i+1, title: t.title, replies: t.posts_count - 1, likes: t.like_count, views: t.views, url: 'https://linux.do/t/topic/' + t.id};
    });
  } catch(e) {
    var rows = document.querySelectorAll('tr.topic-list-item');
    var list = [];
    rows.forEach(function(row, i) {
      var a = row.querySelector('.main-link a.title');
      var replies = row.querySelector('.num.posts span');
      var views = row.querySelector('.num.views span');
      if (a) list.push({rank: i+1, title: a.textContent.trim(), replies: replies ? parseInt(replies.textContent) : 0, views: views ? views.textContent.trim() : '', url: 'https://linux.do' + a.getAttribute('href')});
    });
    return list.slice(0, 25);
  }
})()
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
		Description: "获取雪球热帖（需要登录雪球）",
		URL:         "https://xueqiu.com",
		Tags:        []string{"builtin", "xueqiu", "金融", "热帖", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
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

func xiaohongshuHot() models.Script {
	return models.Script{
		ID:          "builtin-xiaohongshu-hot",
		Name:        "xiaohongshu-hot",
		Description: "获取小红书热门笔记（需要登录）",
		URL:         "https://www.xiaohongshu.com/explore",
		Tags:        []string{"builtin", "xiaohongshu", "热门", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "xiaohongshu_hot",
		MCPCommandDescription: "获取小红书热门笔记（需要登录）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.xiaohongshu.com/explore"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('[class*="note-item"], [class*="feeds-page"] section, a[href*="/explore/"]');
if (!items.length) items = document.querySelectorAll('.note-item, section.note-item');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('[class*="title"], .title, span');
  var authorEl = el.querySelector('[class*="author"], [class*="name"]');
  var likeEl = el.querySelector('[class*="like"], [class*="count"]');
  var linkEl = el.querySelector('a[href*="/explore/"], a[href*="/discovery/"]');
  if (titleEl && titleEl.textContent.trim()) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      author: authorEl ? authorEl.textContent.trim() : '',
      likes: likeEl ? likeEl.textContent.trim() : '',
      url: linkEl ? linkEl.href : '',
    });
  }
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func bossHot() models.Script {
	return models.Script{
		ID:          "builtin-boss-recommend",
		Name:        "boss-recommend",
		Description: "获取 Boss 直聘推荐职位（需要登录）",
		URL:         "https://www.zhipin.com/web/geek/job-recommend",
		Tags:        []string{"builtin", "boss", "招聘", "推荐", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "boss_recommend",
		MCPCommandDescription: "获取 Boss 直聘推荐职位（需要登录）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.zhipin.com/web/geek/job-recommend"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.job-card-wrap, [class*="job-card"], li.job-card');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.job-name, [class*="job-name"], [class*="title"]');
  var companyEl = el.querySelector('.company-name, [class*="company"]');
  var salaryEl = el.querySelector('.salary, [class*="salary"]');
  var areaEl = el.querySelector('.job-area, [class*="area"]');
  var linkEl = el.querySelector('a[href*="/job_detail/"]');
  if (titleEl) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      company: companyEl ? companyEl.textContent.trim() : '',
      salary: salaryEl ? salaryEl.textContent.trim() : '',
      area: areaEl ? areaEl.textContent.trim() : '',
      url: linkEl ? linkEl.href : '',
    });
  }
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func jdSearch() models.Script {
	return models.Script{
		ID:          "builtin-jd-search",
		Name:        "jd-search",
		Description: "京东商品搜索（需要登录以查看价格）",
		URL:         "https://search.jd.com/Search",
		Tags:        []string{"builtin", "jd", "京东", "搜索", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "jd_search",
		MCPCommandDescription: "京东商品搜索（需要登录以查看价格）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"keyword"},
		},
		Variables: map[string]string{"keyword": "手机"},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://search.jd.com/Search?keyword=${keyword}&enc=utf-8"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "search_results",
				JSCode: `
var items = document.querySelectorAll('.gl-item, li.gl-item, [class*="gl-item"]');
if (!items.length) items = document.querySelectorAll('#J_goodsList li');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.p-name a em, .p-name a, [class*="p-name"]');
  var priceEl = el.querySelector('.p-price i, .p-price strong, [class*="p-price"]');
  var shopEl = el.querySelector('.p-shop a, [class*="p-shop"]');
  var linkEl = el.querySelector('a[href*="item.jd.com"]');
  if (titleEl && i < 20) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      price: priceEl ? priceEl.textContent.trim() : '',
      shop: shopEl ? shopEl.textContent.trim() : '',
      url: linkEl ? (linkEl.href.startsWith('//') ? 'https:' + linkEl.href : linkEl.href) : '',
    });
  }
});
return JSON.stringify(list);
`,
			},
		},
	}
}

func taobaoSearch() models.Script {
	return models.Script{
		ID:          "builtin-taobao-search",
		Name:        "taobao-search",
		Description: "淘宝商品搜索（需要登录）",
		URL:         "https://s.taobao.com/search",
		Tags:        []string{"builtin", "taobao", "淘宝", "搜索", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "taobao_search",
		MCPCommandDescription: "淘宝商品搜索（需要登录）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"keyword"},
		},
		Variables: map[string]string{"keyword": "手机壳"},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://s.taobao.com/search?q=${keyword}"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "search_results",
				JSCode: `
var items = document.querySelectorAll('[class*="Card--"], [class*="item"], .item');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('[class*="Title"], .title, a span');
  var priceEl = el.querySelector('[class*="price"], .price, [class*="Price"]');
  var shopEl = el.querySelector('[class*="shop"], [class*="Store"]');
  var linkEl = el.querySelector('a[href*="item.taobao"], a[href*="detail.tmall"]');
  if (titleEl && titleEl.textContent.trim() && i < 20) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      price: priceEl ? priceEl.textContent.trim() : '',
      shop: shopEl ? shopEl.textContent.trim() : '',
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

func twitterTrending() models.Script {
	return models.Script{
		ID:          "builtin-twitter-trending",
		Name:        "twitter-trending",
		Description: "获取 Twitter/X 热门趋势（需要登录）",
		URL:         "https://x.com/explore/tabs/trending",
		Tags:        []string{"builtin", "twitter", "trending", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "twitter_trending",
		MCPCommandDescription: "获取 Twitter/X 热门趋势（需要登录）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://x.com/explore/tabs/trending"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('[data-testid="trend"], [class*="trend"]');
var list = [];
items.forEach(function(el, i) {
  var spans = el.querySelectorAll('span');
  var title = '';
  var tweets = '';
  spans.forEach(function(s) {
    var txt = s.textContent.trim();
    if (txt.startsWith('#') || (txt.length > 2 && !txt.includes('Trending') && !txt.includes('posts'))) {
      if (!title) title = txt;
    }
    if (txt.includes('posts') || txt.includes('K') || txt.includes('M')) {
      tweets = txt;
    }
  });
  if (title) {
    list.push({ rank: i + 1, title: title, tweets: tweets });
  }
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func linkedinJobs() models.Script {
	return models.Script{
		ID:          "builtin-linkedin-jobs",
		Name:        "linkedin-jobs",
		Description: "获取 LinkedIn 推荐职位（需要登录）",
		URL:         "https://www.linkedin.com/jobs/",
		Tags:        []string{"builtin", "linkedin", "招聘", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "linkedin_jobs",
		MCPCommandDescription: "获取 LinkedIn 推荐职位（需要登录）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.linkedin.com/jobs/"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.job-card-container, [class*="job-card"], .jobs-search-results__list-item');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.job-card-list__title, [class*="job-title"], a[class*="title"]');
  var companyEl = el.querySelector('.job-card-container__company-name, [class*="company"]');
  var locationEl = el.querySelector('.job-card-container__metadata-item, [class*="location"]');
  var linkEl = el.querySelector('a[href*="/jobs/view/"]');
  if (titleEl && i < 20) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      company: companyEl ? companyEl.textContent.trim() : '',
      location: locationEl ? locationEl.textContent.trim() : '',
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

func smzdmHot() models.Script {
	return models.Script{
		ID:          "builtin-smzdm-hot",
		Name:        "smzdm-hot",
		Description: "获取什么值得买热门好价",
		URL:         "https://www.smzdm.com/jingxuan/",
		Tags:        []string{"builtin", "smzdm", "优惠", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "smzdm_hot",
		MCPCommandDescription: "获取什么值得买热门好价",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.smzdm.com/jingxuan/"},
			{Type: "sleep", Duration: 4000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
(async function() {
  for (var i = 0; i < 15; i++) {
    if (document.querySelectorAll('.feed-block, [class*="feed-block"], article, li[class*="feed"]').length > 2) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var items = document.querySelectorAll('.feed-block, [class*="feed-block"], article, li[class*="feed"]');
  var list = [];
  items.forEach(function(el, i) {
    var titleEl = el.querySelector('.feed-block__title a, h5 a, [class*="title"] a, a[class*="title"]');
    if (!titleEl) titleEl = el.querySelector('a');
    var priceEl = el.querySelector('.z-highlight, [class*="price"]');
    var mallEl = el.querySelector('.feed-block__mall, [class*="mall"]');
    if (titleEl && list.length < 30) {
      var title = titleEl.textContent.replace(/\s+/g,' ').trim();
      if (title.length > 4 && title.length < 200) {
        list.push({rank: list.length+1, title: title, price: priceEl ? priceEl.textContent.trim() : '', mall: mallEl ? mallEl.textContent.trim() : '', url: titleEl.href || ''});
      }
    }
  });
  return list;
})()
`,
			},
		},
	}
}

func weixinArticle() models.Script {
	return models.Script{
		ID:          "builtin-weixin-hot",
		Name:        "weixin-hot",
		Description: "获取微信公众号热门文章（需要登录微信读书）",
		URL:         "https://weread.qq.com/web/category/rising",
		Tags:        []string{"builtin", "weixin", "公众号", "热门", "需要登录"},
		Group:       "内置脚本",
		CanFetch:      true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "weixin_hot",
		MCPCommandDescription: "获取微信读书飙升榜（需要登录）",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://weread.qq.com/web/category/rising"},
			{Type: "sleep", Duration: 3000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.ranking_bookList_item, [class*="ranking_book"], [class*="bookList"] li');
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.ranking_bookList_title, [class*="title"]');
  var authorEl = el.querySelector('.ranking_bookList_author, [class*="author"]');
  var linkEl = el.querySelector('a');
  if (titleEl && i < 30) {
    list.push({
      rank: i + 1,
      title: titleEl.textContent.trim(),
      author: authorEl ? authorEl.textContent.trim() : '',
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

func jueJinHot() models.Script {
	return models.Script{
		ID:          "builtin-juejin-hot",
		Name:        "juejin-hot",
		Description: "获取掘金热门文章",
		URL:         "https://juejin.cn/hot/articles",
		Tags:        []string{"builtin", "juejin", "技术", "热门"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "juejin_hot",
		MCPCommandDescription: "获取掘金热门文章",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://juejin.cn/hot/articles"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
var items = document.querySelectorAll('.hot-list-item, [class*="HotList"], [class*="hot-item"]');
if (!items.length) items = document.querySelectorAll('a[href*="/post/"]');
var seen = {};
var list = [];
items.forEach(function(el, i) {
  var titleEl = el.querySelector('.article-title, [class*="title"], .hot-list-item-title');
  var hotEl = el.querySelector('[class*="hot"], [class*="count"]');
  var linkEl = el.querySelector('a[href*="/post/"]') || (el.tagName === 'A' ? el : null);
  var title = titleEl ? titleEl.textContent.trim() : (el.textContent || '').trim().slice(0, 60);
  if (title && !seen[title]) {
    seen[title] = true;
    list.push({
      rank: list.length + 1,
      title: title,
      hot: hotEl ? hotEl.textContent.trim() : '',
      url: linkEl ? linkEl.href : '',
    });
  }
});
return JSON.stringify(list.slice(0, 30));
`,
			},
		},
	}
}

func toutiaoHot() models.Script {
	return models.Script{
		ID:          "builtin-toutiao-hot",
		Name:        "toutiao-hot",
		Description: "获取今日头条热榜",
		URL:         "https://www.toutiao.com",
		Tags:        []string{"builtin", "toutiao", "头条", "热榜"},
		Group:       "内置脚本",
		CanFetch:    true,
		IsMCPCommand:          true,
		MCPCommandName:        "toutiao_hot",
		MCPCommandDescription: "获取今日头条热榜",
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.toutiao.com"},
			{Type: "sleep", Duration: 2000},
			{
				Type: "evaluate", VariableName: "hot_list",
				JSCode: `
(async function() {
  try {
    var resp = await fetch('/hot-event/hot-board/?origin=toutiao_pc');
    var data = await resp.json();
    if (data && data.data && data.data.length > 0) {
      return data.data.map(function(item, i) {
        return {rank: i+1, title: item.Title || '', heat: item.HotValue || 0, url: item.Url || ''};
      }).filter(function(item) { return item.title; }).slice(0, 50);
    }
    if (data && data.fixed_top_data) {
      return data.fixed_top_data.map(function(item, i) {
        return {rank: i+1, title: item.Title || '', heat: item.HotValue || 0, url: item.Url || ''};
      }).slice(0, 5);
    }
  } catch(e) {}
  var items = document.querySelectorAll('[class*="hot"] a, a[href*="/trending/"]');
  var seen = {}, list = [];
  items.forEach(function(el) {
    var title = el.textContent.trim();
    var href = el.href || '';
    if (title && title.length > 2 && title.length < 80 && !seen[title]) {
      seen[title] = true;
      list.push({rank: list.length+1, title: title, url: href});
    }
  });
  return list.slice(0, 30);
})()
`,
			},
		},
	}
}

func xiaohongshuPublish() models.Script {
	return models.Script{
		ID:          "builtin-xiaohongshu-publish",
		Name:        "xiaohongshu-publish",
		Description: "小红书发布图文笔记（需要登录创作者中心）",
		URL:         "https://creator.xiaohongshu.com/publish/publish",
		Tags:        []string{"builtin", "xiaohongshu", "发布", "publish", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    false,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "xiaohongshu_publish",
		MCPCommandDescription: "小红书发布图文笔记（需要登录创作者中心）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":  map[string]interface{}{"type": "string", "description": "笔记标题（最多20字）"},
				"content": map[string]interface{}{"type": "string", "description": "笔记正文内容"},
				"images": map[string]interface{}{"type": "string", "description": "图片路径，逗号分隔，支持本地路径或HTTP URL，最多9张（jpg/png/gif/webp）"},
				"topics": map[string]interface{}{"type": "string", "description": "话题标签，逗号分隔，不含#号（可选）"},
				"draft":  map[string]interface{}{"type": "string", "description": "设为 true 则保存为草稿（可选，默认直接发布）"},
			},
			"required": []string{"title", "content", "images"},
		},
		Variables: map[string]string{
			"title":   "",
			"content": "",
			"images":  "",
			"topics":  "",
			"draft":   "false",
		},
		Actions: []models.ScriptAction{
			// Step 1: Navigate to creator publish page
			{Type: "navigate", URL: "https://creator.xiaohongshu.com/publish/publish?from=menu_left"},
			{Type: "sleep", Duration: 3000},

			// Step 2: Select "图文" tab and verify we're on the right page
			{
				Type: "evaluate", VariableName: "_select_tab",
				JSCode: `
(async function() {
  var url = location.href;
  if (!url.includes('creator.xiaohongshu.com')) {
    return JSON.stringify({error: "未在创作者中心，可能未登录。请先登录 creator.xiaohongshu.com"});
  }
  var nodes = document.querySelectorAll('button, [role="tab"], [role="button"], a, label, div, span, li');
  var targets = ['上传图文', '图文', '图片'];
  for (var t = 0; t < targets.length; t++) {
    for (var i = 0; i < nodes.length; i++) {
      var el = nodes[i];
      if (!el || el.offsetParent === null) continue;
      var text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim();
      if (!text || text.includes('视频')) continue;
      if (text === targets[t] || text.startsWith(targets[t]) || text.includes(targets[t])) {
        var clickable = el.closest('button, [role="tab"], [role="button"], a, label') || el;
        clickable.click();
        return JSON.stringify({ok: true, clicked: text});
      }
    }
  }
  return JSON.stringify({ok: true, note: "no tab switch needed"});
})()
`,
			},
			{Type: "sleep", Duration: 2000},

			// Step 3: Upload images via file input
			{
				Type:      "upload_file",
				Selector:  `input[type="file"]`,
				FilePaths: []string{"${images}"},
				Multiple:  true,
			},
			{Type: "sleep", Duration: 8000},

			// Step 4: Wait for editor form and fill title
			{
				Type: "evaluate", VariableName: "_fill_title",
				JSCode: `
(async function() {
  var title = "${title}";
  if (!title) return JSON.stringify({error: "title is empty"});
  var maxWait = 15000, elapsed = 0;
  while (elapsed < maxWait) {
    var el = document.querySelector('input[placeholder*="标题"]')
           || document.querySelector('input.d-text')
           || document.querySelector('[contenteditable="true"][placeholder*="标题"]');
    if (el && el.offsetParent !== null) {
      el.focus();
      if (el.isContentEditable) {
        el.textContent = '';
        document.execCommand('insertText', false, title);
        el.dispatchEvent(new Event('input', {bubbles: true}));
      } else {
        var nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
        nativeSetter.call(el, title);
        el.dispatchEvent(new Event('input', {bubbles: true}));
        el.dispatchEvent(new Event('change', {bubbles: true}));
      }
      await new Promise(function(r) { setTimeout(r, 200); });
      return JSON.stringify({ok: true, title: el.value || el.textContent, selector: el.className});
    }
    await new Promise(function(r) { setTimeout(r, 1000); });
    elapsed += 1000;
  }
  return JSON.stringify({error: "title input not found after waiting"});
})()
`,
			},
			{Type: "sleep", Duration: 500},

			// Step 5: Fill content body (tiptap ProseMirror editor)
			{
				Type: "evaluate", VariableName: "_fill_content",
				JSCode: `
(async function() {
  var content = "${content}";
  if (!content) return JSON.stringify({error: "content is empty"});
  var el = document.querySelector('.ProseMirror[contenteditable="true"]')
        || document.querySelector('.tiptap[contenteditable="true"]')
        || document.querySelector('[contenteditable="true"][class*="editor"]')
        || document.querySelector('[contenteditable="true"]:not(input)');
  if (!el || el.offsetParent === null) {
    return JSON.stringify({error: "content editor not found"});
  }
  el.focus();
  el.innerHTML = '';
  var dt = new DataTransfer();
  dt.setData('text/plain', content);
  var pe = new ClipboardEvent('paste', {bubbles: true, cancelable: true, clipboardData: dt});
  el.dispatchEvent(pe);
  await new Promise(function(r) { setTimeout(r, 300); });
  if (!el.textContent) {
    document.execCommand('insertText', false, content);
  }
  el.dispatchEvent(new Event('input', {bubbles: true}));
  return JSON.stringify({ok: true, length: el.textContent.length});
})()
`,
			},
			{Type: "sleep", Duration: 500},

			// Step 6: Add topic hashtags (if provided)
			{
				Type: "evaluate", VariableName: "_add_topics",
				JSCode: `
(async function() {
  var topicsStr = "${topics}";
  if (!topicsStr) return JSON.stringify({ok: true, note: "no topics"});
  var topics = topicsStr.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
  var added = [];
  for (var t = 0; t < topics.length; t++) {
    var candidates = document.querySelectorAll('*');
    var btnClicked = false;
    for (var i = 0; i < candidates.length; i++) {
      var el = candidates[i];
      var text = (el.innerText || el.textContent || '').trim();
      if ((text === '添加话题' || text === '# 话题' || text.startsWith('添加话题')) &&
          el.offsetParent !== null && el.children.length === 0) {
        el.click();
        btnClicked = true;
        break;
      }
    }
    if (!btnClicked) {
      var hashBtn = document.querySelector('[class*="topic"][class*="btn"], [class*="hashtag"][class*="btn"]');
      if (hashBtn) { hashBtn.click(); btnClicked = true; }
    }
    if (!btnClicked) continue;
    await new Promise(function(r) { setTimeout(r, 1000); });
    var input = document.querySelector('[class*="topic"] input, [class*="hashtag"] input, input[placeholder*="搜索话题"]');
    if (!input || input.offsetParent === null) continue;
    input.focus();
    document.execCommand('insertText', false, topics[t]);
    input.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(r) { setTimeout(r, 1500); });
    var item = document.querySelector('[class*="topic-item"], [class*="hashtag-item"], [class*="suggest-item"], [class*="suggestion"] li');
    if (item) { item.click(); added.push(topics[t]); }
    await new Promise(function(r) { setTimeout(r, 500); });
  }
  return JSON.stringify({ok: true, added: added});
})()
`,
			},
			{Type: "sleep", Duration: 1000},

			// Step 7: Click publish or save draft
			{
				Type: "evaluate", VariableName: "publish_result",
				JSCode: `
(async function() {
  var isDraft = "${draft}" === "true";
  var labels = isDraft ? ['暂存离开', '存草稿'] : ['发布', '发布笔记'];
  var buttons = document.querySelectorAll('button, [role="button"]');
  var clicked = false;
  for (var i = 0; i < buttons.length; i++) {
    var btn = buttons[i];
    var text = (btn.innerText || btn.textContent || '').trim();
    if (btn.offsetParent !== null && !btn.disabled) {
      for (var j = 0; j < labels.length; j++) {
        if (text === labels[j] || text.includes(labels[j])) {
          btn.click();
          clicked = true;
          break;
        }
      }
    }
    if (clicked) break;
  }
  if (!clicked) return JSON.stringify({error: "publish/draft button not found"});
  await new Promise(function(r) { setTimeout(r, 4000); });
  var finalUrl = location.href;
  var successMarkers = isDraft ? ['草稿已保存', '暂存成功', '保存成功'] : ['发布成功', '上传成功'];
  var successMsg = '';
  var allEls = document.querySelectorAll('*');
  for (var k = 0; k < allEls.length; k++) {
    var t = (allEls[k].innerText || '').trim();
    if (allEls[k].children.length === 0) {
      for (var m = 0; m < successMarkers.length; m++) {
        if (t.includes(successMarkers[m])) { successMsg = t; break; }
      }
    }
    if (successMsg) break;
  }
  var navigatedAway = !finalUrl.includes('/publish/publish');
  var ok = successMsg.length > 0 || navigatedAway;
  return JSON.stringify({
    success: ok,
    status: ok ? (isDraft ? "草稿已保存" : "发布成功") : "请在浏览器中确认结果",
    title: "${title}",
    url: finalUrl,
    message: successMsg || ""
  });
})()
`,
			},
		},
	}
}

func twitterReply() models.Script {
	return models.Script{
		ID:          "builtin-twitter-reply",
		Name:        "twitter-reply",
		Description: "Reply to a tweet on X/Twitter (login required)",
		URL:         "https://x.com",
		Tags:        []string{"builtin", "twitter", "x", "reply", "publish", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    false,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "twitter_reply",
		MCPCommandDescription: "Reply to a tweet on X/Twitter (login required)",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url":    map[string]interface{}{"type": "string", "description": "Tweet URL to reply to (e.g. https://x.com/user/status/123)"},
				"text":   map[string]interface{}{"type": "string", "description": "Reply text content"},
				"images": map[string]interface{}{"type": "string", "description": "Image paths, comma-separated, max 4 (optional)"},
			},
			"required": []string{"url", "text"},
		},
		Variables: map[string]string{"url": "", "text": "", "images": ""},
		Actions: []models.ScriptAction{
			{Type: "evaluate", VariableName: "_compose_url",
				JSCode: `(function(){ var u="${url}"; var m=u.match(/status[/](\d+)/); if(!m) return JSON.stringify({error:"Invalid tweet URL"}); return JSON.stringify({ok:true,id:m[1]}); })()`},
			{Type: "navigate", URL: "https://x.com/compose/post?in_reply_to=${url}"},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "_type_reply",
				JSCode: `
(async function() {
  var text = "${text}";
  var maxWait = 8000, elapsed = 0;
  var box = null;
  while (elapsed < maxWait) {
    box = document.querySelector('[data-testid="tweetTextarea_0"]');
    if (box) break;
    await new Promise(function(r){ setTimeout(r, 500); });
    elapsed += 500;
  }
  if (!box) return JSON.stringify({error: "Reply composer not found"});
  box.focus();
  var dt = new DataTransfer();
  dt.setData('text/plain', text);
  box.dispatchEvent(new ClipboardEvent('paste', {clipboardData: dt, bubbles: true, cancelable: true}));
  return JSON.stringify({ok: true});
})()
`},
			{Type: "upload_file", Selector: `input[data-testid="fileInput"]`, FilePaths: []string{"${images}"}, Multiple: true},
			{Type: "sleep", Duration: 2000},
			{Type: "evaluate", VariableName: "reply_result",
				JSCode: `
(async function() {
  var images = "${images}";
  if (images && images.trim()) {
    var maxW = 30000, el = 0;
    while (el < maxW) {
      var c = document.querySelector('[data-testid="attachments"]');
      var btn = document.querySelector('[data-testid="tweetButton"]') || document.querySelector('[data-testid="tweetButtonInline"]');
      if (c && btn && !btn.disabled) break;
      await new Promise(function(r){ setTimeout(r, 500); });
      el += 500;
    }
  }
  await new Promise(function(r){ setTimeout(r, 1000); });
  var btn = document.querySelector('[data-testid="tweetButton"]') || document.querySelector('[data-testid="tweetButtonInline"]');
  if (!btn || btn.disabled) return JSON.stringify({success: false, error: "Reply button disabled or not found"});
  btn.click();
  await new Promise(function(r){ setTimeout(r, 3000); });
  return JSON.stringify({success: !location.href.includes('/compose/'), status: "Reply sent"});
})()
`},
		},
	}
}

func xiaohongshuSearch() models.Script {
	return models.Script{
		ID:          "builtin-xiaohongshu-search",
		Name:        "xiaohongshu-search",
		Description: "搜索小红书笔记（需要登录）",
		URL:         "https://www.xiaohongshu.com/search_result",
		Tags:        []string{"builtin", "xiaohongshu", "搜索", "search", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "xiaohongshu_search",
		MCPCommandDescription: "搜索小红书笔记（需要登录）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"keyword"},
		},
		Variables: map[string]string{"keyword": ""},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.xiaohongshu.com/search_result?keyword=${keyword}&source=web_search_result_notes"},
			{Type: "sleep", Duration: 4000},
			{Type: "evaluate", VariableName: "search_results",
				JSCode: `
(async function() {
  await new Promise(function(r){ setTimeout(r, 2000); });
  var items = document.querySelectorAll('section.note-item, [class*="note-item"], .feeds-page .note-item');
  if (!items.length) items = document.querySelectorAll('a[href*="/explore/"]');
  var list = [], seen = {};
  items.forEach(function(el, i) {
    if (i >= 20) return;
    var titleEl = el.querySelector('.title, .note-title, [class*="title"]');
    var authorEl = el.querySelector('.author-wrapper .name, .author .name, [class*="author"] .name, [class*="nickname"]');
    var likesEl = el.querySelector('.like-wrapper .count, [class*="like"] .count, [class*="like-count"]');
    var linkEl = el.querySelector('a[href*="/explore/"], a[href*="/discovery/item/"]');
    var title = titleEl ? titleEl.textContent.trim() : '';
    var href = linkEl ? linkEl.href : (el.querySelector('a') ? el.querySelector('a').href : '');
    if (!title && el.textContent) title = el.textContent.trim().split('\n')[0].substring(0, 60);
    if (title && !seen[title]) {
      seen[title] = true;
      list.push({
        rank: list.length + 1,
        title: title,
        author: authorEl ? authorEl.textContent.trim() : '',
        likes: likesEl ? likesEl.textContent.trim() : '',
        url: href
      });
    }
  });
  return list;
})()
`},
		},
	}
}

func xiaohongshuNote() models.Script {
	return models.Script{
		ID:          "builtin-xiaohongshu-note",
		Name:        "xiaohongshu-note",
		Description: "获取小红书笔记详情（标题/正文/点赞/收藏/评论）",
		URL:         "https://www.xiaohongshu.com",
		Tags:        []string{"builtin", "xiaohongshu", "笔记", "note", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "xiaohongshu_note",
		MCPCommandDescription: "获取小红书笔记详情（标题/正文/点赞/收藏/评论）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{"type": "string", "description": "笔记完整URL（含xsec_token）"},
			},
			"required": []string{"url"},
		},
		Variables: map[string]string{"url": ""},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "${url}"},
			{Type: "sleep", Duration: 4000},
			{Type: "evaluate", VariableName: "note_detail",
				JSCode: `
(async function() {
  await new Promise(function(r){ setTimeout(r, 1000); });
  var title = '', content = '', author = '', likes = '', collects = '', comments = '';
  var t = document.querySelector('#detail-title, .title, .note-title');
  if (t) title = t.textContent.trim();
  var d = document.querySelector('#detail-desc, .desc, .note-desc, [class*="note-text"]');
  if (d) content = d.textContent.trim();
  var u = document.querySelector('.username, .author .name, [class*="user-name"]');
  if (u) author = u.textContent.trim();
  var interact = document.querySelector('.interact-container, .note-detail .engage-bar, [class*="interact"]');
  var scope = interact || document;
  var likeEl = scope.querySelector('.like-wrapper .count, [class*="like"] .count, [data-testid="like-count"]');
  if (likeEl) likes = likeEl.textContent.trim();
  var collectEl = scope.querySelector('.collect-wrapper .count, [class*="collect"] .count');
  if (collectEl) collects = collectEl.textContent.trim();
  var commentEl = scope.querySelector('.chat-wrapper .count, [class*="comment"] .count');
  if (commentEl) comments = commentEl.textContent.trim();
  var tags = [];
  document.querySelectorAll('a.tag, [class*="tag"] a, a[href*="search_result"]').forEach(function(a) {
    var txt = a.textContent.trim().replace(/^#/, '');
    if (txt && tags.indexOf(txt) === -1) tags.push(txt);
  });
  return {title: title, author: author, content: content.substring(0, 2000), likes: likes, collects: collects, comments: comments, tags: tags, url: location.href};
})()
`},
		},
	}
}

func xiaohongshuUser() models.Script {
	return models.Script{
		ID:          "builtin-xiaohongshu-user",
		Name:        "xiaohongshu-user",
		Description: "获取小红书用户主页笔记列表（需要登录）",
		URL:         "https://www.xiaohongshu.com/user/profile/",
		Tags:        []string{"builtin", "xiaohongshu", "用户", "user", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "xiaohongshu_user",
		MCPCommandDescription: "获取小红书用户主页笔记列表（需要登录）",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"user_id": map[string]interface{}{"type": "string", "description": "用户ID或用户主页URL"},
			},
			"required": []string{"user_id"},
		},
		Variables: map[string]string{"user_id": ""},
		Actions: []models.ScriptAction{
			{Type: "evaluate", VariableName: "_nav_url",
				JSCode: `(function(){ var id="${user_id}"; if(id.startsWith("http")) return JSON.stringify({url:id}); return JSON.stringify({url:"https://www.xiaohongshu.com/user/profile/"+id}); })()`},
			{Type: "navigate", URL: "https://www.xiaohongshu.com/user/profile/${user_id}"},
			{Type: "sleep", Duration: 4000},
			{Type: "evaluate", VariableName: "user_notes",
				JSCode: `
(async function() {
  await new Promise(function(r){ setTimeout(r, 1000); });
  var state = window.__INITIAL_STATE__;
  if (state && state.user && state.user.notes) {
    var notes = [];
    var raw = state.user.notes;
    if (Array.isArray(raw)) {
      raw.forEach(function(n, i) {
        if (i >= 30) return;
        notes.push({
          rank: i + 1,
          id: n.id || n.note_id || '',
          title: n.display_title || n.title || '',
          type: n.type === 'video' ? 'video' : 'image',
          likes: n.liked_count || n.likes || 0,
          url: 'https://www.xiaohongshu.com/explore/' + (n.id || n.note_id || '')
        });
      });
    }
    if (notes.length > 0) return notes;
  }
  var items = document.querySelectorAll('[class*="note-item"] a, .note-item a, section.note-item a');
  var list = [], seen = {};
  items.forEach(function(a, i) {
    if (i >= 30 || !a.href) return;
    var title = a.textContent.trim().split('\n')[0].substring(0, 60);
    if (title && !seen[a.href]) {
      seen[a.href] = true;
      list.push({rank: list.length + 1, title: title, url: a.href});
    }
  });
  return list;
})()
`},
		},
	}
}

func twitterSearch() models.Script {
	return models.Script{
		ID:          "builtin-twitter-search",
		Name:        "twitter-search",
		Description: "Search tweets on X/Twitter (login required)",
		URL:         "https://x.com/search",
		Tags:        []string{"builtin", "twitter", "x", "search", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "twitter_search",
		MCPCommandDescription: "Search tweets on X/Twitter (login required)",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search query"},
			},
			"required": []string{"query"},
		},
		Variables: map[string]string{"query": ""},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://x.com/search?q=${query}&src=typed_query&f=top"},
			{Type: "sleep", Duration: 4000},
			{Type: "evaluate", VariableName: "search_results",
				JSCode: `
(async function() {
  await new Promise(function(r){ setTimeout(r, 2000); });
  var articles = document.querySelectorAll('article[data-testid="tweet"]');
  var list = [];
  articles.forEach(function(el, i) {
    if (i >= 20) return;
    var userEl = el.querySelector('[data-testid="User-Name"]');
    var textEl = el.querySelector('[data-testid="tweetText"]');
    var timeEl = el.querySelector('time');
    var linkEl = el.querySelector('a[href*="/status/"]');
    var author = '', handle = '';
    if (userEl) {
      var spans = userEl.querySelectorAll('span');
      for (var s = 0; s < spans.length; s++) {
        var t = spans[s].textContent.trim();
        if (t.startsWith('@')) handle = t;
        else if (t && !author && t !== '·' && !t.match(/^\d/)) author = t;
      }
    }
    var replyEl = el.querySelector('[data-testid="reply"] span, [data-testid="reply"] [dir]');
    var retweetEl = el.querySelector('[data-testid="retweet"] span, [data-testid="retweet"] [dir]');
    var likeEl = el.querySelector('[data-testid="like"] span, [data-testid="like"] [dir]');
    list.push({
      rank: i + 1,
      author: author,
      handle: handle,
      text: textEl ? textEl.textContent.trim().substring(0, 280) : '',
      time: timeEl ? timeEl.getAttribute('datetime') : '',
      replies: replyEl ? replyEl.textContent.trim() : '0',
      retweets: retweetEl ? retweetEl.textContent.trim() : '0',
      likes: likeEl ? likeEl.textContent.trim() : '0',
      url: linkEl ? 'https://x.com' + linkEl.getAttribute('href') : ''
    });
  });
  return list;
})()
`},
		},
	}
}

func twitterProfile() models.Script {
	return models.Script{
		ID:          "builtin-twitter-profile",
		Name:        "twitter-profile",
		Description: "Get X/Twitter user profile info (login required)",
		URL:         "https://x.com",
		Tags:        []string{"builtin", "twitter", "x", "profile", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    true,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "twitter_profile",
		MCPCommandDescription: "Get X/Twitter user profile info (login required)",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"username": map[string]interface{}{"type": "string", "description": "Twitter username (without @)"},
			},
			"required": []string{"username"},
		},
		Variables: map[string]string{"username": ""},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://x.com/${username}"},
			{Type: "sleep", Duration: 4000},
			{Type: "evaluate", VariableName: "profile_info",
				JSCode: `
(async function() {
  await new Promise(function(r){ setTimeout(r, 1000); });
  var name = '', bio = '', location = '', website = '', joined = '';
  var followers = '', following = '', verified = false;
  var nameEl = document.querySelector('[data-testid="UserName"] span span');
  if (nameEl) name = nameEl.textContent.trim();
  var bioEl = document.querySelector('[data-testid="UserDescription"]');
  if (bioEl) bio = bioEl.textContent.trim();
  var locEl = document.querySelector('[data-testid="UserLocation"]');
  if (locEl) location = locEl.textContent.trim();
  var urlEl = document.querySelector('[data-testid="UserUrl"] a');
  if (urlEl) website = urlEl.textContent.trim();
  var joinEl = document.querySelector('[data-testid="UserJoinDate"]');
  if (joinEl) joined = joinEl.textContent.trim();
  var badge = document.querySelector('[data-testid="UserName"] svg[data-testid="icon-verified"]');
  if (badge) verified = true;
  var links = document.querySelectorAll('a[href*="/verified_followers"], a[href*="/followers"], a[href*="/following"]');
  links.forEach(function(a) {
    var text = a.textContent.trim();
    var href = a.getAttribute('href') || '';
    if (href.includes('/following')) following = text.replace(/Following.*/, '').trim();
    else if (href.includes('/followers') || href.includes('/verified_followers')) followers = text.replace(/Follower.*/, '').trim();
  });
  return {
    name: name,
    username: "${username}",
    bio: bio.substring(0, 500),
    location: location,
    website: website,
    joined: joined,
    followers: followers,
    following: following,
    verified: verified,
    url: location.href
  };
})()
`},
		},
	}
}

func twitterPost() models.Script {
	return models.Script{
		ID:          "builtin-twitter-post",
		Name:        "twitter-post",
		Description: "Post a tweet on X/Twitter (login required)",
		URL:         "https://x.com/compose/tweet",
		Tags:        []string{"builtin", "twitter", "x", "post", "publish", "需要登录"},
		Group:       "内置脚本",
		CanFetch:    false,
		RequiresLogin: true,
		IsMCPCommand:          true,
		MCPCommandName:        "twitter_post",
		MCPCommandDescription: "Post a tweet on X/Twitter (login required)",
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text":   map[string]interface{}{"type": "string", "description": "Tweet text content"},
				"images": map[string]interface{}{"type": "string", "description": "Image paths, comma-separated, max 4 (jpg/png/gif/webp). Optional."},
			},
			"required": []string{"text"},
		},
		Variables: map[string]string{
			"text":   "",
			"images": "",
		},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://x.com/compose/tweet"},
			{Type: "sleep", Duration: 3000},

			// Step 1: Verify login and type text via clipboard paste (handles Draft.js)
			{
				Type: "evaluate", VariableName: "_type_text",
				JSCode: `
(async function() {
  var text = "${text}";
  if (!text) return JSON.stringify({error: "text is empty"});
  if (location.href.includes('/login') || location.href.includes('/i/flow')) {
    return JSON.stringify({error: "Not logged in. Please login to x.com first."});
  }
  var maxWait = 8000, elapsed = 0;
  var box = null;
  while (elapsed < maxWait) {
    box = document.querySelector('[data-testid="tweetTextarea_0"]');
    if (box) break;
    await new Promise(function(r) { setTimeout(r, 500); });
    elapsed += 500;
  }
  if (!box) return JSON.stringify({error: "Tweet composer not found. Page may not have loaded."});
  box.focus();
  var dt = new DataTransfer();
  dt.setData('text/plain', text);
  box.dispatchEvent(new ClipboardEvent('paste', {clipboardData: dt, bubbles: true, cancelable: true}));
  return JSON.stringify({ok: true});
})()
`,
			},
			{Type: "sleep", Duration: 1000},

			// Step 2: Upload images if provided
			{
				Type: "evaluate", VariableName: "_upload_check",
				JSCode: `
(function() {
  var images = "${images}";
  return JSON.stringify({has_images: images && images.trim().length > 0});
})()
`,
			},
			{
				Type:      "upload_file",
				Selector:  `input[data-testid="fileInput"]`,
				FilePaths: []string{"${images}"},
				Multiple:  true,
			},
			{Type: "sleep", Duration: 3000},

			// Step 3: Wait for uploads to finish, then click post
			{
				Type: "evaluate", VariableName: "post_result",
				JSCode: `
(async function() {
  var images = "${images}";
  var hasImages = images && images.trim().length > 0;
  if (hasImages) {
    var maxWait = 30000, elapsed = 0;
    while (elapsed < maxWait) {
      var container = document.querySelector('[data-testid="attachments"]');
      if (container) {
        var btn = document.querySelector('[data-testid="tweetButton"]') || document.querySelector('[data-testid="tweetButtonInline"]');
        if (btn && !btn.disabled) break;
      }
      await new Promise(function(r) { setTimeout(r, 500); });
      elapsed += 500;
    }
  }
  await new Promise(function(r) { setTimeout(r, 1000); });
  var btn = document.querySelector('[data-testid="tweetButton"]') || document.querySelector('[data-testid="tweetButtonInline"]');
  if (!btn) return JSON.stringify({success: false, error: "Tweet button not found"});
  if (btn.disabled) return JSON.stringify({success: false, error: "Tweet button is disabled. Content may be invalid."});
  btn.click();
  await new Promise(function(r) { setTimeout(r, 3000); });
  var url = location.href;
  var composed = url.includes('/compose/');
  return JSON.stringify({
    success: !composed,
    status: composed ? "Tweet may not have been sent. Please verify in browser." : "Tweet posted successfully",
    url: url
  });
})()
`,
			},
		},
	}
}
