package builtin

import "github.com/browserwing/browserwing/models"

func searchTemplate(id, name, desc, url string, tags []string, requiresLogin bool, paramName, paramDesc string, jsCode string) models.Script {
	s := scriptTemplate(id, name, desc, url, tags, requiresLogin, jsCode)
	s.Variables = map[string]string{paramName: ""}
	s.MCPInputSchema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			paramName: map[string]interface{}{
				"type":        "string",
				"description": paramDesc,
			},
		},
		"required": []string{paramName},
	}
	s.Actions = []models.ScriptAction{
		{Type: "navigate", URL: url + "${" + paramName + "}"},
		{Type: "sleep", Duration: 4000},
		{Type: "evaluate", VariableName: "result", JSCode: jsCode},
	}
	return s
}

// --- Finance ---

func thsHotRank() models.Script {
	return scriptTemplate("ths-hot-rank", "ths-hot-rank", "同花顺热股排行榜", "https://eq.10jqka.com.cn/webpage/ths-hot-list/index.html?showStatusBar=true", []string{"ths", "finance", "stock"}, false, `
var cards = document.querySelectorAll('div.pt-22, div[class*="bgc-white"]');
var results = [], seen = {};
cards.forEach(function(card, idx) {
  var row = card.querySelector('div.flex') || card;
  var nameEl = row.querySelector('span.ellipsis') || row.querySelector('span[class*="name"]');
  var name = nameEl ? nameEl.textContent.replace(/\s+/g,' ').trim() : '';
  if (!name || seen[name]) return;
  seen[name] = true;
  var rankEl = row.querySelector('div.bold, div[class*="rank"]');
  var changeEl = row.querySelector('div.range, div[class*="change"]');
  var heatEl = row.querySelector('div.col4 > span, span[class*="heat"]');
  results.push({rank: rankEl ? rankEl.textContent.trim() : String(idx+1), name: name, change: changeEl ? changeEl.textContent.trim() : '', heat: heatEl ? heatEl.textContent.trim() : ''});
});
return results.slice(0, 30);
`)
}

func tdxHotRank() models.Script {
	return scriptTemplate("tdx-hot-rank", "tdx-hot-rank", "通达信热搜排行榜", "https://pul.tdx.com.cn/site/app/gzhbd/tdx-topsearch/page-main.html?pageName=page_topsearch&tabClickIndex=0&subtabIndex=0", []string{"tdx", "finance", "stock"}, false, `
var cells = document.querySelectorAll('div.top-cell[data-code]');
var results = [], seen = {};
cells.forEach(function(cell, idx) {
  var symbol = cell.getAttribute('data-code') || '';
  var name = cell.getAttribute('data-name') || '';
  if (!symbol || !name || seen[symbol]) return;
  seen[symbol] = true;
  var changeEl = cell.querySelector('div.top-zf');
  var heatEl = cell.querySelector('div.hotN');
  results.push({rank: idx+1, symbol: symbol, name: name, change: changeEl ? changeEl.textContent.trim() : '', heat: heatEl ? heatEl.textContent.trim() : ''});
});
return results.slice(0, 30);
`)
}

func yahooFinanceQuote() models.Script {
	return searchTemplate("yahoo-finance-quote", "yahoo-finance-quote", "Yahoo Finance stock quote", "https://finance.yahoo.com/quote/", []string{"yahoo", "finance", "stock"}, false, "symbol", "Stock ticker symbol (e.g. AAPL, TSLA, MSFT)", `
(async function() {
  try {
    var sym = location.pathname.split('/quote/')[1]?.split('/')[0] || '';
    var chartUrl = 'https://query1.finance.yahoo.com/v8/finance/chart/' + encodeURIComponent(sym) + '?interval=1d&range=1d';
    var resp = await fetch(chartUrl);
    if (resp.ok) {
      var d = await resp.json();
      var meta = d?.chart?.result?.[0]?.meta || {};
      var price = meta.regularMarketPrice;
      var prevClose = meta.previousClose || meta.chartPreviousClose;
      var change = price != null && prevClose != null ? (price - prevClose).toFixed(2) : null;
      var changePct = change != null && prevClose ? ((change / prevClose) * 100).toFixed(2) + '%' : null;
      return [{symbol: meta.symbol || sym, name: meta.shortName || meta.longName || sym, price: price, change: change, changePercent: changePct, high: meta.regularMarketDayHigh, low: meta.regularMarketDayLow, volume: meta.regularMarketVolume, currency: meta.currency, exchange: meta.exchangeName}];
    }
  } catch(e) {}
  var priceEl = document.querySelector('[data-testid="qsp-price"]');
  var titleEl = document.querySelector('title');
  if (priceEl) {
    return [{symbol: location.pathname.split('/quote/')[1]?.split('/')[0] || '', name: titleEl ? titleEl.textContent.split('(')[0].trim() : '', price: priceEl.textContent.replace(/,/g,''), change: null, changePercent: null}];
  }
  return [{error: 'Could not fetch quote'}];
})()
`)
}

func barchartOptions() models.Script {
	return searchTemplate("barchart-options", "barchart-options", "Barchart US stock options chain", "https://www.barchart.com/stocks/quotes/", []string{"barchart", "finance", "options"}, false, "symbol", "Stock ticker symbol (e.g. AAPL, TSLA)", `
(async function() {
  var sym = location.pathname.split('/quotes/')[1]?.split('/')[0] || '';
  var csrf = document.querySelector('meta[name="csrf-token"]')?.content || '';
  try {
    var fields = 'strikePrice,bidPrice,askPrice,lastPrice,priceChange,volume,openInterest,volatility,delta,gamma,theta,vega,expirationDate,optionType,percentFromLast';
    var url = '/proxies/core-api/v1/options/chain?symbol=' + encodeURIComponent(sym) + '&fields=' + fields + '&raw=1';
    var resp = await fetch(url, {credentials:'include', headers:{'X-CSRF-TOKEN': csrf}});
    if (resp.ok) {
      var d = await resp.json();
      var items = (d?.data || []).filter(function(i){return ((i.raw||i).optionType||'').toLowerCase()==='call';});
      items.sort(function(a,b){return Math.abs((a.raw||a).percentFromLast||999)-Math.abs((b.raw||b).percentFromLast||999);});
      return items.slice(0,20).map(function(i){var r=i.raw||i;return {strike:r.strikePrice,bid:r.bidPrice,ask:r.askPrice,last:r.lastPrice,volume:r.volume,openInterest:r.openInterest,iv:r.volatility,delta:r.delta,expiration:r.expirationDate};});
    }
  } catch(e) {}
  return [];
})()
`)
}

// --- News ---

func bloombergNews() models.Script {
	return scriptTemplate("bloomberg-rss", "bloomberg-rss", "Bloomberg top headlines via RSS", "https://feeds.bloomberg.com/news.rss", []string{"bloomberg", "news"}, false, `
var items = document.querySelectorAll('item, entry');
var results = [];
items.forEach(function(item, idx) {
  var title = (item.querySelector('title')?.textContent || '').trim();
  var link = (item.querySelector('link')?.textContent || item.querySelector('link')?.getAttribute('href') || '').trim();
  var desc = (item.querySelector('description')?.textContent || '').replace(/<[^>]*>/g,'').trim().slice(0, 200);
  var date = (item.querySelector('pubDate, published, updated')?.textContent || '').trim();
  if (title) results.push({rank: idx+1, title: title, link: link, summary: desc, date: date});
});
return results.slice(0, 20);
`)
}

func reutersSearch() models.Script {
	return searchTemplate("reuters-search", "reuters-search", "Reuters news search", "https://www.reuters.com/site-search/?query=", []string{"reuters", "news"}, false, "query", "News search keyword", `
(async function() {
  var apiQuery = JSON.stringify({keyword: new URLSearchParams(location.search).get('query') || '', offset: 0, orderby: 'display_date:desc', size: 20, website: 'reuters'});
  var apiUrl = 'https://www.reuters.com/pf/api/v3/content/fetch/articles-by-search-v2?query=' + encodeURIComponent(apiQuery);
  try {
    var resp = await fetch(apiUrl, {credentials: 'include'});
    if (resp.ok) {
      var data = await resp.json();
      var articles = data.result?.articles || data.articles || [];
      return articles.slice(0, 20).map(function(a, i) {
        return {rank: i+1, title: a.title || a.headlines?.basic || '', date: (a.display_date || a.published_time || '').split('T')[0], section: a.taxonomy?.section?.name || '', url: a.canonical_url ? 'https://www.reuters.com' + a.canonical_url : ''};
      });
    }
  } catch(e) {}
  var items = document.querySelectorAll('[class*="search-result"], [data-testid*="SearchResult"]');
  var results = [];
  items.forEach(function(el, idx) {
    var a = el.querySelector('a');
    var title = a ? a.textContent.trim() : '';
    var href = a ? a.getAttribute('href') : '';
    if (title) results.push({rank: idx+1, title: title, url: href && !href.startsWith('http') ? 'https://www.reuters.com'+href : href});
  });
  return results.slice(0, 20);
})()
`)
}

func sinablogHot() models.Script {
	return scriptTemplate("sinablog-hot", "sinablog-hot", "新浪博客热门文章", "https://blog.sina.com.cn/", []string{"sinablog", "blog"}, false, `
var links = document.querySelectorAll('a[href*="blog.sina.com.cn/s/blog_"]');
var results = [], seen = {};
links.forEach(function(a, idx) {
  var title = a.textContent.replace(/\s+/g,' ').trim();
  var href = a.getAttribute('href') || '';
  if (!title || title.length < 4 || seen[title]) return;
  seen[title] = true;
  results.push({rank: results.length+1, title: title, url: href});
});
if (results.length === 0) {
  document.querySelectorAll('.atc_title a, .SG_txtb a, .title a').forEach(function(a) {
    var title = a.textContent.replace(/\s+/g,' ').trim();
    var href = a.getAttribute('href') || '';
    if (title && title.length >= 4 && !seen[title]) {
      seen[title] = true;
      results.push({rank: results.length+1, title: title, url: href});
    }
  });
}
return results.slice(0, 20);
`)
}

// --- Entertainment ---

func applePodcastsTop() models.Script {
	return scriptTemplate("apple-podcasts-top", "apple-podcasts-top", "Apple Podcasts top chart", "https://rss.marketingtools.apple.com/api/v2/us/podcasts/top/25/podcasts.json", []string{"apple", "podcasts"}, false, `
try {
  var text = document.body?.innerText || document.querySelector('pre')?.textContent || '';
  var data = JSON.parse(text);
  var results = (data?.feed?.results || []).map(function(p, i) {
    return {rank: i+1, title: p.name, author: p.artistName, id: p.id, url: p.url || ''};
  });
  return results.slice(0, 25);
} catch(e) { return [{error: 'Failed to parse podcast data'}]; }
`)
}

func tiktokExplore() models.Script {
	return scriptTemplate("tiktok-explore", "tiktok-explore", "TikTok Explore trending videos", "https://www.tiktok.com/explore", []string{"tiktok", "trending"}, false, `
var cards = document.querySelectorAll('[data-e2e="explore-item"], [class*="DivItemContainer"], [class*="video-feed-item"]');
var results = [];
cards.forEach(function(card, idx) {
  var a = card.querySelector('a[href*="/video/"]') || card.querySelector('a');
  var desc = card.querySelector('[data-e2e="explore-item-desc"], [class*="ItemDescription"], p')?.textContent?.trim() || '';
  var author = card.querySelector('[data-e2e="explore-item-user"], [class*="Author"], span[class*="user"]')?.textContent?.trim() || '';
  var views = card.querySelector('[class*="play"], [class*="view"]')?.textContent?.trim() || '';
  var href = a ? a.getAttribute('href') : '';
  if (href && !href.startsWith('http')) href = 'https://www.tiktok.com' + href;
  if (desc || href) results.push({rank: idx+1, description: desc.slice(0,100), author: author, views: views, url: href});
});
return results.slice(0, 20);
`)
}

func pixivRanking() models.Script {
	return scriptTemplate("pixiv-ranking", "pixiv-ranking", "Pixiv illustration daily ranking", "https://www.pixiv.net/ranking.php?mode=daily", []string{"pixiv", "illustration"}, true, `
var items = document.querySelectorAll('.ranking-item, [class*="rankingItem"]');
var results = [];
items.forEach(function(item, idx) {
  var titleEl = item.querySelector('.title, a[data-title], h2');
  var title = titleEl ? (titleEl.getAttribute('data-title') || titleEl.textContent || '').trim() : '';
  var userEl = item.querySelector('.user-name, [data-user-name]');
  var user = userEl ? (userEl.getAttribute('data-user-name') || userEl.textContent || '').trim() : '';
  var a = item.querySelector('a[href*="/artworks/"]');
  var href = a ? a.getAttribute('href') : '';
  if (href && !href.startsWith('http')) href = 'https://www.pixiv.net' + href;
  if (title || href) results.push({rank: idx+1, title: title, author: user, url: href});
});
return results.slice(0, 30);
`)
}

// --- Reading ---

func substackFeed() models.Script {
	return scriptTemplate("substack-feed", "substack-feed", "Substack popular articles", "https://substack.com/", []string{"substack", "reading"}, false, `
var cards = document.querySelectorAll('[class*="post-preview"], [class*="PostPreview"], article');
var results = [];
cards.forEach(function(card, idx) {
  var titleEl = card.querySelector('[class*="post-preview-title"], h2, h3');
  var title = titleEl ? titleEl.textContent.trim() : '';
  var authorEl = card.querySelector('[class*="profile-hover"], [class*="author"], [class*="pub-name"]');
  var author = authorEl ? authorEl.textContent.trim() : '';
  var a = card.querySelector('a[href*="/p/"]') || card.querySelector('a');
  var href = a ? a.getAttribute('href') : '';
  var dateEl = card.querySelector('time, [class*="date"]');
  var date = dateEl ? (dateEl.getAttribute('datetime') || dateEl.textContent || '').trim() : '';
  if (title) results.push({rank: idx+1, title: title, author: author, date: date.split('T')[0], url: href});
});
return results.slice(0, 20);
`)
}

func dictionaryLookup() models.Script {
	return searchTemplate("dictionary-lookup", "dictionary-lookup", "English dictionary word lookup", "https://api.dictionaryapi.dev/api/v2/entries/en/", []string{"dictionary", "english"}, false, "word", "English word to look up", `
try {
  var text = document.body?.innerText || document.querySelector('pre')?.textContent || '';
  var data = JSON.parse(text);
  if (!Array.isArray(data)) return [{error: data?.message || 'Word not found'}];
  return data.map(function(entry) {
    var meanings = (entry.meanings || []).map(function(m) {
      return {partOfSpeech: m.partOfSpeech, definitions: (m.definitions || []).slice(0,3).map(function(d){return d.definition;})};
    });
    return {word: entry.word, phonetic: entry.phonetic || '', phonetics: (entry.phonetics||[]).map(function(p){return p.text||'';}).filter(Boolean).join(', '), meanings: meanings};
  });
} catch(e) { return [{error: 'Failed to parse dictionary data'}]; }
`)
}

// --- Academic ---

func googleScholarSearch() models.Script {
	return searchTemplate("google-scholar-search", "google-scholar-search", "Google Scholar paper search", "https://scholar.google.com/scholar?hl=en&q=", []string{"google-scholar", "academic"}, false, "query", "Search keyword for academic papers", `
var items = document.querySelectorAll('.gs_r.gs_or.gs_scl, .gs_ri');
var results = [];
items.forEach(function(el) {
  var container = el.querySelector('.gs_ri') || el;
  var titleEl = container.querySelector('.gs_rt a, h3 a');
  var title = titleEl ? titleEl.textContent.replace(/\s+/g,' ').trim() : '';
  if (!title) return;
  var url = titleEl ? titleEl.getAttribute('href') : '';
  var infoLine = (container.querySelector('.gs_a')?.textContent || '').replace(/\s+/g,' ').trim();
  var parts = infoLine.split(' - ');
  var authors = (parts[0] || '').trim();
  var sourceParts = (parts[1] || '').split(',');
  var source = sourceParts.slice(0,-1).join(',').trim() || sourceParts[0]?.trim() || '';
  var year = (infoLine.match(/(19|20)\d{2}/) || [''])[0];
  var citedText = (container.querySelector('.gs_fl a[href*="cites"]')?.textContent || '');
  var cited = (citedText.match(/(\d+)/) || ['0','0'])[1];
  results.push({rank: results.length+1, title: title, authors: authors.slice(0,80), source: source.slice(0,60), year: year, cited: cited, url: url});
});
return results.slice(0, 20);
`)
}

func baiduScholarSearch() models.Script {
	return searchTemplate("baidu-scholar-search", "baidu-scholar-search", "百度学术论文检索", "https://xueshu.baidu.com/s?wd=", []string{"baidu-scholar", "academic"}, false, "query", "搜索关键词", `
(async function() {
  for (var i = 0; i < 20; i++) {
    if (document.querySelectorAll('.result').length > 0) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [];
  document.querySelectorAll('.result').forEach(function(el) {
    var titleEl = el.querySelector('h3 a, .paper-title a, .t a');
    var title = titleEl ? titleEl.textContent.replace(/\s+/g,' ').trim() : '';
    if (!title) return;
    var url = titleEl ? titleEl.getAttribute('href') : '';
    if (url && !url.startsWith('http')) url = 'https://xueshu.baidu.com' + url;
    var infoText = (el.querySelector('.paper-info')?.textContent || '').replace(/\s+/g,' ').trim();
    var year = (infoText.match(/(19|20)\d{2}/) || [''])[0];
    var cited = (infoText.match(/被引量[：:]\s*(\d+)/) || ['0','0'])[1];
    var journal = '';
    var spans = el.querySelectorAll('.paper-info span');
    spans.forEach(function(span) {
      var t = span.textContent.trim();
      if (t.startsWith('《') || t.startsWith('〈')) journal = t.replace(/[《》〈〉]/g,'');
    });
    results.push({rank: results.length+1, title: title, journal: journal, year: year, cited: cited, url: url});
  });
  return results.slice(0, 20);
})()
`)
}

func wanfangSearch() models.Script {
	return searchTemplate("wanfang-search", "wanfang-search", "万方数据论文搜索", "https://s.wanfangdata.com.cn/paper?q=", []string{"wanfang", "academic"}, false, "query", "搜索关键词", `
(async function() {
  for (var i = 0; i < 30; i++) {
    if (document.querySelectorAll('span.title').length > 0) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [];
  document.querySelectorAll('span.title').forEach(function(titleSpan) {
    var title = titleSpan.textContent.replace(/\s+/g,' ').trim();
    if (!title || title.length < 3) return;
    var container = titleSpan.parentElement;
    for (var i = 0; i < 6; i++) {
      if (!container?.parentElement || container.parentElement.tagName === 'BODY') break;
      if (container.querySelectorAll('span.title').length >= 1) break;
      container = container.parentElement;
    }
    var id = (container.querySelector('span.title-id-hidden')?.textContent || '').trim();
    var url = id ? 'https://d.wanfangdata.com.cn/' + id : '';
    var authors = Array.from(container.querySelectorAll('span.authors')).map(function(s){return s.textContent.trim();}).filter(Boolean).join(', ').slice(0,80);
    var type = (container.querySelector('span.essay-type')?.textContent || '').trim();
    var source = (container.querySelector('span.periodical, span.source')?.textContent || '').trim();
    var year = (container.querySelector('span.year, span.date')?.textContent || '').trim();
    if (!year) year = ((container.textContent||'').match(/(19|20)\d{2}/) || [''])[0];
    results.push({rank: results.length+1, title: title, authors: authors, source: source, year: year, type: type, url: url});
  });
  return results.slice(0, 20);
})()
`)
}

func cnkiSearch() models.Script {
	return searchTemplate("cnki-search", "cnki-search", "中国知网论文搜索", "https://kns.cnki.net/kns2/article/result/navi/retrieval?dbcode=CFLS&kw=", []string{"cnki", "academic"}, false, "query", "搜索关键词", `
(async function() {
  for (var i = 0; i < 40; i++) {
    if (document.querySelectorAll('.result-table-list tbody tr, #gridTable tbody tr, table tbody tr').length > 0) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var rows = document.querySelectorAll('.result-table-list tbody tr, #gridTable tbody tr, table.result-table tbody tr');
  var results = [];
  rows.forEach(function(row) {
    var tds = row.querySelectorAll('td');
    if (tds.length < 3) return;
    var titleEl = row.querySelector('td.name a, td a.fz14, a[href*="detail"]');
    if (!titleEl) titleEl = tds[1]?.querySelector('a') || tds[0]?.querySelector('a');
    var title = titleEl ? titleEl.textContent.replace(/\s+/g,' ').trim().replace(/免费$/,'') : '';
    if (!title || title.length < 3) return;
    var url = titleEl ? titleEl.getAttribute('href') : '';
    if (url && !url.startsWith('http')) url = 'https://kns.cnki.net' + url;
    var authors = (tds[2]?.textContent || '').replace(/\s+/g,' ').trim();
    var journal = (tds[3]?.textContent || '').replace(/\s+/g,' ').trim();
    var date = (tds[4]?.textContent || '').replace(/\s+/g,' ').trim();
    results.push({rank: results.length+1, title: title, authors: authors, journal: journal, date: date, url: url});
  });
  return results.slice(0, 20);
})()
`)
}

// --- Other ---

func googleTrends() models.Script {
	return scriptTemplate("google-trends", "google-trends", "Google Trends daily trending searches", "https://trends.google.com/trending?geo=US&hours=24", []string{"google", "trends"}, false, `
var results = [], seen = {};
document.querySelectorAll('tr, [class*="feed-item"], [class*="trending-item"], [role="row"]').forEach(function(row, idx) {
  var cells = row.querySelectorAll('td, [role="cell"]');
  if (cells.length >= 2) {
    var title = cells[0].textContent.replace(/\s+/g,' ').trim();
    var traffic = cells.length > 1 ? cells[1].textContent.replace(/\s+/g,' ').trim() : '';
    if (title && title.length > 1 && title.length < 80 && !seen[title]) {
      seen[title] = true;
      results.push({rank: results.length+1, title: title, traffic: traffic});
    }
  }
});
if (results.length === 0) {
  document.querySelectorAll('[class*="details"] a, [class*="title"], .mZ3RIc').forEach(function(el) {
    var t = el.textContent.replace(/\s+/g,' ').trim();
    if (t && t.length > 2 && t.length < 80 && !seen[t]) {
      seen[t] = true;
      results.push({rank: results.length+1, title: t});
    }
  });
}
return results.slice(0, 30);
`)
}

func govPolicy() models.Script {
	return scriptTemplate("gov-policy", "gov-policy", "国务院最新政策文件", "https://www.gov.cn/zhengce/zuixin/index.htm", []string{"gov", "policy"}, false, `
(async function() {
  for (var i = 0; i < 30; i++) {
    if (document.querySelectorAll('li a, .news_box a, .list a, .list_item a').length > 5) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [], seen = {};
  document.querySelectorAll('li').forEach(function(el) {
    var a = el.querySelector('a');
    if (!a) return;
    var title = a.textContent.replace(/\s+/g,' ').trim();
    if (!title || title.length < 6 || title.length > 200 || seen[title]) return;
    seen[title] = true;
    var url = a.getAttribute('href') || '';
    if (url && !url.startsWith('http')) url = 'https://www.gov.cn' + url;
    if (!url.includes('gov.cn')) return;
    var date = ((el.textContent||'').match(/(\d{4}[-./]\d{1,2}[-./]\d{1,2})/)||[''])[0];
    results.push({rank: results.length+1, title: title, date: date, url: url});
  });
  return results.slice(0, 30);
})()
`)
}

func govLaw() models.Script {
	return scriptTemplate("gov-law", "gov-law", "国家法律法规数据库最新", "https://flk.npc.gov.cn/fl.html", []string{"gov", "law"}, false, `
(async function() {
  for (var i = 0; i < 30; i++) {
    if (document.querySelectorAll('.el-table__row, .law-item, .result-item, li a[href*="detail"]').length > 2) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [], seen = {};
  document.querySelectorAll('.el-table__row, .law-item, .result-item, tr').forEach(function(el) {
    var cells = el.querySelectorAll('td, .cell');
    var titleEl = el.querySelector('a') || (cells.length > 0 ? cells[0] : null);
    if (!titleEl) return;
    var title = titleEl.textContent.replace(/\s+/g,' ').trim();
    if (!title || title.length < 3 || title.length > 200 || seen[title]) return;
    seen[title] = true;
    var url = '';
    var a = el.querySelector('a');
    if (a) { url = a.getAttribute('href') || ''; }
    if (url && !url.startsWith('http')) url = 'https://flk.npc.gov.cn' + url;
    var date = ((el.textContent||'').match(/(\d{4}[-./]\d{1,2}[-./]\d{1,2})/)||[''])[0];
    results.push({rank: results.length+1, title: title, date: date, url: url});
  });
  if (results.length === 0) {
    document.querySelectorAll('a').forEach(function(a) {
      var title = a.textContent.replace(/\s+/g,' ').trim();
      var href = a.getAttribute('href') || '';
      if (title && title.length > 4 && title.length < 200 && !seen[title] && href.includes('detail')) {
        seen[title] = true;
        results.push({rank: results.length+1, title: title, url: href.startsWith('http') ? href : 'https://flk.npc.gov.cn/' + href});
      }
    });
  }
  return results.slice(0, 30);
})()
`)
}

func ctripSearch() models.Script {
	return searchTemplate("ctrip-search", "ctrip-search", "携程目的地/酒店搜索", "https://www.ctrip.com/hotels/list?keyword=", []string{"ctrip", "travel"}, false, "keyword", "搜索关键词（城市/酒店名）", `
(async function() {
  for (var i = 0; i < 20; i++) {
    if (document.querySelectorAll('[class*="hotel-item"], [class*="list-card"], .hotel_new_list li').length > 0) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [];
  document.querySelectorAll('[class*="hotel-item"], [class*="list-card"], .hotel_new_list li').forEach(function(el) {
    var nameEl = el.querySelector('[class*="hotel-name"], [class*="name"], h2');
    var name = nameEl ? nameEl.textContent.replace(/\s+/g,' ').trim() : '';
    if (!name) return;
    var priceEl = el.querySelector('[class*="price"], [class*="Price"]');
    var price = priceEl ? priceEl.textContent.replace(/\s+/g,' ').trim() : '';
    var scoreEl = el.querySelector('[class*="score"], [class*="Score"]');
    var score = scoreEl ? scoreEl.textContent.trim() : '';
    var a = el.querySelector('a');
    var href = a ? a.getAttribute('href') : '';
    results.push({rank: results.length+1, name: name, price: price, score: score, url: href});
  });
  return results.slice(0, 20);
})()
`)
}

func jianyuSearch() models.Script {
	return searchTemplate("jianyu-search", "jianyu-search", "剑鱼招投标公告检索", "https://www.jianyu360.cn/jylab/supsearch/index.html?keywords=", []string{"jianyu", "bid"}, false, "query", "搜索关键词（如: 信息化建设）", `
(async function() {
  for (var i = 0; i < 30; i++) {
    if (document.querySelectorAll('.result-item, .list-item, [class*="search-result"]').length > 0) break;
    await new Promise(function(r){setTimeout(r, 500);});
  }
  var results = [];
  document.querySelectorAll('.result-item, .list-item, [class*="search-result"], .news_list li').forEach(function(el) {
    var titleEl = el.querySelector('a, .title, h3');
    var title = titleEl ? titleEl.textContent.replace(/\s+/g,' ').trim() : '';
    if (!title || title.length < 4) return;
    var a = el.querySelector('a');
    var href = a ? a.getAttribute('href') : '';
    if (href && !href.startsWith('http')) href = 'https://www.jianyu360.cn' + href;
    var date = ((el.textContent||'').match(/(\d{4}[-./]\d{1,2}[-./]\d{1,2})/)||[''])[0];
    results.push({rank: results.length+1, title: title, date: date, url: href});
  });
  return results.slice(0, 20);
})()
`)
}
