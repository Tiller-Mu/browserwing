package builtin

import "github.com/browserwing/browserwing/models"

func scriptTemplate(id, name, desc, url string, tags []string, requiresLogin bool, jsCode string) models.Script {
	return models.Script{
		ID:                    "builtin-" + id,
		Name:                  name,
		Description:           desc,
		URL:                   url,
		Tags:                  append([]string{"builtin"}, tags...),
		Group:                 "内置脚本",
		CanFetch:              true,
		RequiresLogin:         requiresLogin,
		IsMCPCommand:          true,
		MCPCommandName:        name,
		MCPCommandDescription: desc,
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: url},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "result", JSCode: jsCode},
		},
	}
}

// --- Tech ---

func devtoTop() models.Script {
	return scriptTemplate("devto-top", "devto-top", "Get DEV.to top articles", "https://dev.to/top/week", []string{"devto", "tech"}, false, `
var items = document.querySelectorAll('.crayons-story');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.crayons-story__title a, h2 a');
  var u = el.querySelector('.crayons-story__meta a');
  if (t) list.push({rank: i+1, title: t.textContent.trim(), url: t.href, author: u ? u.textContent.trim() : ''});
});
return JSON.stringify(list.slice(0, 30));
`)
}

func lobstersHot() models.Script {
	return scriptTemplate("lobsters-hot", "lobsters-hot", "Get Lobste.rs hottest stories", "https://lobste.rs/", []string{"lobsters", "tech"}, false, `
var items = document.querySelectorAll('.story');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.link a, .u-url');
  var s = el.querySelector('.score');
  if (t) list.push({rank: i+1, title: t.textContent.trim(), url: t.href, score: s ? s.textContent.trim() : ''});
});
return JSON.stringify(list.slice(0, 30));
`)
}

func hfTop() models.Script {
	return scriptTemplate("hf-trending", "hf-trending", "Get Hugging Face trending models", "https://huggingface.co/models?sort=trending", []string{"huggingface", "ai"}, false, `
var items = document.querySelectorAll('article.overview-card-wrapper, [class*="ModelCard"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('h4, [class*="card-title"]');
  var a = el.querySelector('a');
  var d = el.querySelector('[class*="downloads"], [title*="downloads"]');
  if (t) list.push({rank: i+1, name: t.textContent.trim(), url: a ? a.href : '', downloads: d ? d.textContent.trim() : ''});
});
return JSON.stringify(list.slice(0, 30));
`)
}

func arxivNew() models.Script {
	return scriptTemplate("arxiv-new", "arxiv-new", "Get latest arXiv CS.AI papers", "https://arxiv.org/list/cs.AI/recent", []string{"arxiv", "papers"}, false, `
var items = document.querySelectorAll('#dlpage dt, .list-identifier');
var titles = document.querySelectorAll('#dlpage dd .list-title, .mathjax');
var list = [];
var dts = document.querySelectorAll('#dlpage dt');
var dds = document.querySelectorAll('#dlpage dd');
for (var i = 0; i < Math.min(dts.length, 30); i++) {
  var link = dts[i].querySelector('a[href*="/abs/"]');
  var titleEl = dds[i] ? dds[i].querySelector('.list-title') : null;
  if (link && titleEl) {
    list.push({rank: i+1, title: titleEl.textContent.replace('Title:','').trim(), url: 'https://arxiv.org' + link.getAttribute('href')});
  }
}
return JSON.stringify(list);
`)
}

func giteeHot() models.Script {
	return scriptTemplate("gitee-trending", "gitee-trending", "获取 Gitee 热门开源项目", "https://gitee.com/explore/all", []string{"gitee", "开源"}, false, `
var items = document.querySelectorAll('.explore-repo__list .item, .project-list .project');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.project-title a, .title a, h3 a');
  var d = el.querySelector('.project-desc, .desc, p');
  var s = el.querySelector('[class*="star"], .stars-count');
  if (t) list.push({rank: i+1, name: t.textContent.trim(), url: t.href, description: d ? d.textContent.trim() : '', stars: s ? s.textContent.trim() : ''});
});
return JSON.stringify(list.slice(0, 30));
`)
}

func nowcoderHot() models.Script {
	return scriptTemplate("nowcoder-hot", "nowcoder-hot", "获取牛客网热门讨论", "https://www.nowcoder.com/feed/main/detail", []string{"nowcoder", "求职"}, false, `
(async function() {
  for (var i = 0; i < 15; i++) {
    if (document.querySelectorAll('[class*="feed-item"], [class*="discuss-item"], .nk-list-item, a[href*="/discuss/"]').length > 2) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var list = [], seen = {};
  document.querySelectorAll('a[href*="/discuss/"], a[href*="/feed/main/detail/"]').forEach(function(a) {
    var title = a.textContent.replace(/\s+/g,' ').trim();
    var href = a.href || a.getAttribute('href') || '';
    if (!title || title.length < 6 || title.length > 150 || seen[title]) return;
    if (/^\d+$/.test(title) || title === '讨论' || title === '查看') return;
    seen[title] = true;
    if (!href.startsWith('http')) href = 'https://www.nowcoder.com' + href;
    list.push({rank: list.length+1, title: title, url: href});
  });
  return list.slice(0, 30);
})()
`)
}

// --- News ---

func bbcNews() models.Script {
	return scriptTemplate("bbc-news", "bbc-news", "Get BBC News top stories", "https://www.bbc.com/news", []string{"bbc", "news"}, false, `
var items = document.querySelectorAll('[data-testid="edinburgh-card"], .gs-c-promo, article');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('h2, h3, [data-testid="card-headline"]');
  var a = el.querySelector('a');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), url: a ? a.href : ''});
});
return JSON.stringify(list);
`)
}

func googleNews() models.Script {
	return scriptTemplate("google-news", "google-news", "Get Google News top stories", "https://news.google.com/", []string{"google", "news"}, false, `
var items = document.querySelectorAll('article, c-wiz article');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('a[class*="gPFEn"], h3 a, h4 a, a');
  if (t && t.textContent.trim().length > 5 && list.length < 20) {
    list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href});
  }
});
return JSON.stringify(list);
`)
}

// --- Entertainment ---

func steamTopSellers() models.Script {
	return scriptTemplate("steam-topsellers", "steam-topsellers", "Get Steam top sellers", "https://store.steampowered.com/search/?filter=topsellers", []string{"steam", "games"}, false, `
var items = document.querySelectorAll('#search_resultsRows a');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.title');
  var p = el.querySelector('.discount_final_price, .search_price');
  if (t && list.length < 30) list.push({rank: list.length+1, name: t.textContent.trim(), url: el.href, price: p ? p.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

func youtubetrending() models.Script {
	return scriptTemplate("youtube-trending", "youtube-trending", "Get YouTube trending videos", "https://www.youtube.com/feed/trending", []string{"youtube", "trending"}, false, `
var items = document.querySelectorAll('ytd-video-renderer, ytd-rich-item-renderer');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('#video-title');
  var c = el.querySelector('#channel-name a, ytd-channel-name a');
  var v = el.querySelector('#metadata-line span');
  if (t && list.length < 30) list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href || '', channel: c ? c.textContent.trim() : '', views: v ? v.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

// --- Finance ---

func binanceTop() models.Script {
	return scriptTemplate("binance-gainers", "binance-gainers", "Get Binance top gainers (24h)", "https://www.binance.com/en/markets/gainersAndLosers?type=zone&tradeType=spot", []string{"binance", "crypto"}, false, `
var items = document.querySelectorAll('tr[class*="css"]');
var list = [];
items.forEach(function(el, i) {
  var cells = el.querySelectorAll('td');
  if (cells.length >= 3) {
    var name = cells[0].textContent.trim();
    var price = cells[1] ? cells[1].textContent.trim() : '';
    var change = cells[2] ? cells[2].textContent.trim() : '';
    if (name && name !== 'Name' && list.length < 30) list.push({rank: list.length+1, name: name, price: price, change_24h: change});
  }
});
return JSON.stringify(list);
`)
}

// --- Social ---

func blueskyTrending() models.Script {
	return scriptTemplate("bluesky-trending", "bluesky-trending", "Get Bluesky trending posts", "https://bsky.app/", []string{"bluesky", "social"}, true, `
var items = document.querySelectorAll('[data-testid="feedItem"], [data-testid="postThreadItem"]');
var list = [];
items.forEach(function(el, i) {
  var author = el.querySelector('[data-testid="profileHeaderDisplayName"], [class*="displayName"]');
  var text = el.querySelector('[data-testid="postText"], [class*="postText"]');
  var likes = el.querySelector('[data-testid="likeCount"], [aria-label*="like"]');
  if (text && list.length < 20) list.push({rank: list.length+1, author: author ? author.textContent.trim() : '', text: text.textContent.trim().substring(0, 200), likes: likes ? likes.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

func jikeHot() models.Script {
	return scriptTemplate("jike-hot", "jike-hot", "获取即刻热门动态", "https://web.okjike.com/", []string{"jike", "社交"}, true, `
(async function() {
  for (var i = 0; i < 20; i++) {
    var cards = document.querySelectorAll('[class*="MessageCard"], [class*="message-card"], [class*="feed-item"], [class*="feedItem"], article');
    if (cards.length > 2) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var items = document.querySelectorAll('[class*="MessageCard"], [class*="message-card"], [class*="feed-item"], [class*="feedItem"], article');
  var list = [];
  items.forEach(function(el) {
    var author = el.querySelector('[class*="username"], [class*="UserName"], [class*="displayName"], [class*="nickName"]');
    var text = el.querySelector('[class*="content"], [class*="text"], [class*="body"] p, p');
    if (text && list.length < 20) {
      var txt = text.textContent.replace(/\s+/g,' ').trim().substring(0, 200);
      if (txt.length > 5) list.push({rank: list.length+1, author: author ? author.textContent.trim() : '', text: txt});
    }
  });
  return list;
})()
`)
}

// --- Reading ---

func wereadRanking() models.Script {
	return scriptTemplate("weread-ranking", "weread-ranking", "获取微信读书飙升榜", "https://weread.qq.com/web/category/rising", []string{"weread", "读书"}, false, `
var items = document.querySelectorAll('.book_item, [class*="bookItem"], .wr_bookList_item');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.book_title, [class*="bookTitle"], .wr_bookList_item_title');
  var a = el.querySelector('.book_author, [class*="bookAuthor"], .wr_bookList_item_author');
  var link = el.querySelector('a');
  if (t && list.length < 30) list.push({rank: list.length+1, title: t.textContent.trim(), author: a ? a.textContent.trim() : '', url: link ? link.href : ''});
});
return JSON.stringify(list);
`)
}

func mediumTrending() models.Script {
	return scriptTemplate("medium-trending", "medium-trending", "Get Medium trending articles", "https://medium.com/tag/technology/recommended", []string{"medium", "reading"}, false, `
var items = document.querySelectorAll('article');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('h2, h3');
  var a = el.querySelector('a[data-testid="authorName"], a[rel="author"]');
  var link = el.querySelector('a[data-testid="postPreview"]') || el.querySelector('h2 a, h3 a');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), author: a ? a.textContent.trim() : '', url: link ? link.href : ''});
});
return JSON.stringify(list);
`)
}

// --- Shopping ---

func amazonBestsellers() models.Script {
	return scriptTemplate("amazon-bestsellers", "amazon-bestsellers", "Get Amazon bestsellers", "https://www.amazon.com/gp/bestsellers/", []string{"amazon", "shopping"}, false, `
var items = document.querySelectorAll('.zg-grid-general-faceout, .a-carousel-card, [id^="gridItemRoot"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.p13n-sc-truncate, ._cDEzb_p13n-sc-css-line-clamp-3_g3dy1, .a-link-normal span');
  var p = el.querySelector('.p13n-sc-price, ._cDEzb_p13n-sc-price_3mJ9Z, .a-price .a-offscreen');
  var link = el.querySelector('a.a-link-normal');
  if (t && list.length < 30) list.push({rank: list.length+1, name: t.textContent.trim(), price: p ? p.textContent.trim() : '', url: link ? link.href : ''});
});
return JSON.stringify(list);
`)
}

func xianyuHot() models.Script {
	return scriptTemplate("xianyu-hot", "xianyu-hot", "获取闲鱼热卖商品", "https://www.goofish.com/", []string{"xianyu", "二手"}, true, `
var items = document.querySelectorAll('[class*="feed-item"], [class*="card"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('[class*="title"], h3');
  var p = el.querySelector('[class*="price"]');
  var link = el.querySelector('a');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), price: p ? p.textContent.trim() : '', url: link ? link.href : ''});
});
return JSON.stringify(list);
`)
}

// --- Jobs ---

func maimaiSearch() models.Script {
	return scriptTemplate("maimai-hot", "maimai-hot", "获取脉脉热门话题", "https://maimai.cn/web/gossip_list", []string{"maimai", "职场"}, true, `
var items = document.querySelectorAll('[class*="gossip-item"], [class*="feed-card"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('[class*="title"], h3');
  var v = el.querySelector('[class*="view"], [class*="count"]');
  var link = el.querySelector('a');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), views: v ? v.textContent.trim() : '', url: link ? link.href : ''});
});
return JSON.stringify(list);
`)
}

// --- Search / Academic ---

func wikipediaTrending() models.Script {
	return scriptTemplate("wikipedia-trending", "wikipedia-trending", "Get Wikipedia most viewed articles today", "https://en.wikipedia.org/wiki/Wikipedia:Top_25_Report", []string{"wikipedia", "trending"}, false, `
var items = document.querySelectorAll('.wikitable tr');
var list = [];
items.forEach(function(el, i) {
  if (i === 0) return;
  var cells = el.querySelectorAll('td');
  if (cells.length >= 3) {
    var link = cells[1] ? cells[1].querySelector('a') : null;
    var views = cells[2] ? cells[2].textContent.trim() : '';
    if (link && list.length < 25) list.push({rank: list.length+1, title: link.textContent.trim(), url: 'https://en.wikipedia.org' + link.getAttribute('href'), views: views});
  }
});
return JSON.stringify(list);
`)
}

// --- Other / Miscellaneous ---

func lesswrongCurated() models.Script {
	return scriptTemplate("lesswrong-curated", "lesswrong-curated", "Get LessWrong curated posts", "https://www.lesswrong.com/allPosts?filter=curated", []string{"lesswrong", "rationality"}, false, `
var items = document.querySelectorAll('[class*="PostsItem"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('a[class*="PostsTitle"], [class*="title"] a');
  var k = el.querySelector('[class*="karma"]');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href, karma: k ? k.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

func keErshoufang() models.Script {
	return scriptTemplate("ke-ershoufang", "ke-ershoufang", "获取贝壳找房热门二手房", "https://bj.ke.com/ershoufang/", []string{"ke", "房产"}, false, `
var items = document.querySelectorAll('.sellListContent li, .listContent .clear');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.title a, .lj-lazy');
  var p = el.querySelector('.totalPrice span, .priceInfo .totalPrice');
  var info = el.querySelector('.houseInfo, .address');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href, price: p ? p.textContent.trim() + '万' : '', info: info ? info.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

func doubanBookHot() models.Script {
	return scriptTemplate("douban-book-hot", "douban-book-hot", "获取豆瓣热门图书", "https://book.douban.com/chart", []string{"douban", "book"}, false, `
var items = document.querySelectorAll('.chart-dashed-list li, .article .item');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('a.fleft, .title a, h2 a');
  var r = el.querySelector('.rating_nums, [class*="rating"]');
  if (t && list.length < 30) list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href, rating: r ? r.textContent.trim() : ''});
});
return JSON.stringify(list);
`)
}

func zhihuDaily() models.Script {
	return scriptTemplate("zhihu-daily", "zhihu-daily", "获取知乎日报", "https://daily.zhihu.com/", []string{"zhihu", "daily"}, false, `
var items = document.querySelectorAll('.main-content a, .box a');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('span, p');
  if (!t) t = el;
  var text = t.textContent.trim();
  if (text.length > 5 && text.length < 100 && el.href && list.length < 20) list.push({rank: list.length+1, title: text, url: el.href});
});
return JSON.stringify(list);
`)
}

func weiboSearch() models.Script {
	return models.Script{
		ID:                    "builtin-weibo-search",
		Name:                  "weibo-search",
		Description:           "搜索微博内容",
		URL:                   "https://s.weibo.com/weibo?q=${keyword}",
		Tags:                  []string{"builtin", "weibo", "搜索"},
		Group:                 "内置脚本",
		CanFetch:              true,
		RequiresLogin:         true,
		IsMCPCommand:          true,
		MCPCommandName:        "weibo_search",
		MCPCommandDescription: "搜索微博内容",
		Variables:             map[string]string{"keyword": ""},
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"keyword"},
		},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://s.weibo.com/weibo?q=${keyword}"},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "result", JSCode: `
var items = document.querySelectorAll('[action-type="feed_list_item"], .card-wrap');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('[class*="txt"], p[node-type="feed_list_content"]');
  var a = el.querySelector('.name, [class*="name"]');
  if (t && list.length < 20) list.push({rank: list.length+1, content: t.textContent.trim().substring(0, 200), author: a ? a.textContent.trim() : ''});
});
return JSON.stringify(list);
`},
		},
	}
}

func doubanSearch() models.Script {
	return models.Script{
		ID:                    "builtin-douban-search",
		Name:                  "douban-search",
		Description:           "搜索豆瓣电影",
		URL:                   "https://search.douban.com/movie/subject_search?search_text=${keyword}",
		Tags:                  []string{"builtin", "douban", "搜索"},
		Group:                 "内置脚本",
		CanFetch:              true,
		IsMCPCommand:          true,
		MCPCommandName:        "douban_search",
		MCPCommandDescription: "搜索豆瓣电影",
		Variables:             map[string]string{"keyword": ""},
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "电影名称"},
			},
			"required": []string{"keyword"},
		},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://search.douban.com/movie/subject_search?search_text=${keyword}"},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "result", JSCode: `
var items = document.querySelectorAll('.item-root, .sc-bZQynM');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('.title-text, h3 a, a.title-text');
  var r = el.querySelector('.rating_nums, [class*="rating"]');
  var meta = el.querySelector('.meta, [class*="meta"]');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), url: t.href || '', rating: r ? r.textContent.trim() : '', info: meta ? meta.textContent.trim() : ''});
});
return JSON.stringify(list);
`},
		},
	}
}

func githubSearch() models.Script {
	return models.Script{
		ID:                    "builtin-github-search",
		Name:                  "github-search",
		Description:           "Search GitHub repositories",
		URL:                   "https://github.com/search?q=${keyword}&type=repositories&s=stars",
		Tags:                  []string{"builtin", "github", "search"},
		Group:                 "内置脚本",
		CanFetch:              true,
		IsMCPCommand:          true,
		MCPCommandName:        "github_search",
		MCPCommandDescription: "Search GitHub repositories by keyword",
		Variables:             map[string]string{"keyword": ""},
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "Search keyword"},
			},
			"required": []string{"keyword"},
		},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://github.com/search?q=${keyword}&type=repositories&s=stars"},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "result", JSCode: `
var items = document.querySelectorAll('[data-testid="results-list"] > div, .Box-row, .repo-list-item, [class*="search-title"]');
var list = [];
items.forEach(function(el) {
  var t = el.querySelector('a[href*="/"] span, a.v-align-middle, a[class*="prc-Link"], .search-title a');
  if (!t) t = el.querySelector('a[href*="/"]');
  var d = el.querySelector('p, [class*="description"], .search-match');
  var star = '';
  var s = el.querySelector('[class*="octicon-star"], svg.octicon-star');
  if (s && s.parentElement) star = s.parentElement.textContent.replace(/\s+/g,'').trim();
  if (t && list.length < 20) list.push({rank: list.length+1, name: t.textContent.replace(/\s+/g,' ').trim(), url: t.closest('a') ? t.closest('a').href : (t.href || ''), description: d ? d.textContent.trim().slice(0,120) : '', stars: star});
});
return list;
`},
		},
	}
}

func zhihuSearch() models.Script {
	return models.Script{
		ID:                    "builtin-zhihu-search",
		Name:                  "zhihu-search",
		Description:           "搜索知乎问答",
		URL:                   "https://www.zhihu.com/search?type=content&q=${keyword}",
		Tags:                  []string{"builtin", "zhihu", "搜索"},
		Group:                 "内置脚本",
		CanFetch:              true,
		IsMCPCommand:          true,
		MCPCommandName:        "zhihu_search",
		MCPCommandDescription: "搜索知乎问答",
		Variables:             map[string]string{"keyword": ""},
		MCPInputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"keyword"},
		},
		Actions: []models.ScriptAction{
			{Type: "navigate", URL: "https://www.zhihu.com/search?type=content&q=${keyword}"},
			{Type: "sleep", Duration: 3000},
			{Type: "evaluate", VariableName: "result", JSCode: `
var items = document.querySelectorAll('.SearchResult-Card, [class*="SearchItem"]');
var list = [];
items.forEach(function(el, i) {
  var t = el.querySelector('h2 a span, [class*="title"]');
  var link = el.querySelector('h2 a');
  var excerpt = el.querySelector('[class*="content"], [class*="RichText"]');
  if (t && list.length < 20) list.push({rank: list.length+1, title: t.textContent.trim(), url: link ? link.href : '', excerpt: excerpt ? excerpt.textContent.trim().substring(0, 150) : ''});
});
return JSON.stringify(list);
`},
		},
	}
}
