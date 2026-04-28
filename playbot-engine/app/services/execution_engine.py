import logging
import time
from typing import Dict, Any, List, Optional
from playwright.sync_api import Page, Locator
from pydantic import BaseModel

logger = logging.getLogger(__name__)

class ResolveError(Exception):
    def __init__(self, step: dict, message: str):
        self.step = step
        self.message = message
        super().__init__(self.message)

class StepExecutionError(Exception):
    def __init__(self, step_index: int, step: dict, message: str, original_error: Exception = None):
        self.step_index = step_index
        self.step = step
        self.message = message
        self.original_error = original_error
        super().__init__(f"Step {step_index} failed: {self.message}")

class PlaybotExecutionEngine:
    def __init__(self, page: Page, credentials: Optional[Dict[str, str]] = None):
        self.page = page
        self.credentials = credentials

    def _auto_login_if_needed(self):
        if not self.credentials:
            return
            
        username = self.credentials.get("username")
        password = self.credentials.get("password")
        login_url = self.credentials.get("login_url")
        
        if not username or not password:
            return
            
        try:
            current_url = self.page.url
            if "login" in current_url.lower() or (login_url and login_url in current_url):
                logger.info("拦截到登录态缺失，正在执行启发式自动登录...")
                
                username_loc = self.page.get_by_placeholder("用户名")
                if username_loc.count() == 0:
                     username_loc = self.page.locator("input[type='text'], input[name='username'], input[id*='user']")
                if username_loc.count() > 0:
                    username_loc.first.fill(username)
                    
                password_loc = self.page.locator("input[type='password']")
                if password_loc.count() > 0:
                    password_loc.first.fill(password)
                    
                submit_btn = self.page.locator("button[type='submit'], button:has-text('登录'), button:has-text('Login')")
                if submit_btn.count() > 0:
                    submit_btn.first.click()
                    self.page.wait_for_load_state("networkidle")
                    logger.info("自动登录执行完毕")
        except Exception as e:
            logger.warning(f"自动登录尝试失败: {e}")

    def execute_plan(self, steps: List[dict]):
        self._auto_login_if_needed()
        
        # 记录当前页面状态和即将执行的计划
        try:
            logger.info(f"Starting plan execution. Current URL: {self.page.url}, Steps count: {len(steps)}")
            for i, s in enumerate(steps):
                sd = s if isinstance(s, dict) else dict(s)
                logger.debug(f"  Step {i}: action={sd.get('action')}, intent={sd.get('intent_reason')}, value={sd.get('value')}")
        except Exception:
            pass
        
        for index, step_data in enumerate(steps):
            step = step_data if isinstance(step_data, dict) else dict(step_data)
            
            try:
                logger.info(f"Executing step {index}: {step.get('action')} - {step.get('intent_reason')}")
                
                start_resolve = time.time()
                locator = self._resolve_locator(step)
                resolve_time = time.time() - start_resolve
                logger.info(f"[Timer] _resolve_locator took {resolve_time:.2f} seconds")
                
                if locator is None and step.get("action") not in ["navigate", "custom_script", "virtual_navigate", "switch_view"]:
                    if step.get("action") == "expect_visible":
                        page_info = ""
                        try:
                            page_info = f"\n当前页面 URL: {self.page.url}"
                        except Exception:
                            pass
                        raise AssertionError(f"UI 断言失败：预期的元素（{step.get('intent_reason', '未知')}）在超时时间内未出现。\n寻址目标: {step.get('target_hint')}{page_info}")
                    else:
                        page_info = ""
                        try:
                            page_info = f"\n当前页面 URL: {self.page.url}"
                        except Exception:
                            pass
                        raise ResolveError(step, f"UI 寻址失败：无法找到操作目标（{step.get('intent_reason', '未知')}）。\n寻址目标: {step.get('target_hint')}{page_info}")
                    
                start_exec = time.time()
                self._execute_action(locator, step)
                exec_time = time.time() - start_exec
                logger.info(f"[Timer] _execute_action took {exec_time:.2f} seconds")
                
                self._post_validate(step)
            except Exception as e:
                if isinstance(e, StepExecutionError):
                    raise
                raise StepExecutionError(step_index=index, step=step, message=str(e), original_error=e)

    def _get_scopes(self, step: dict) -> List[Locator]:
        scopes = []
        target_component = step.get("target_component")
        
        # 1. 组件沙盒（最高优先级）
        if target_component:
            scopes.append(
                self.page.locator(f'[data-playbot-component="{target_component}"]')
            )

        # 2. 结构沙盒作为回退
        scopes.extend([
            self.page.locator("form"),
            self.page.locator("main"),
            self.page.locator("body")
        ])

        return scopes

    def _collect_candidates(self, scope: Locator, step: dict) -> List[dict]:
        target_hint = step.get("target_hint", {})
        if not target_hint:
            return []

        tag = target_hint.get("tag") or "*"
        hint_text = target_hint.get("text") or ""
        hint_role = target_hint.get("role") or ""
        hint_placeholder = target_hint.get("placeholder") or ""
        
        # 处理单引号防止 JS 报错
        hint_text_js = hint_text.replace("'", "\\'")
        
        try:
            candidates_data = scope.evaluate(f'''(scopeNode) => {{
                const elements = Array.from(scopeNode.querySelectorAll('{tag}'));
                const hintText = '{hint_text_js}';
                const hintRole = '{hint_role}';
                const hintPlaceholder = '{hint_placeholder}';
                
                let results = [];
                const hasHint = hintText || hintRole || hintPlaceholder;
                
                for (let i = 0; i < elements.length; i++) {{
                    let el = elements[i];
                    let elText = el.textContent || el.value || '';
                    
                    // 恢复“软打分”的预过滤：只要命中了任何一个线索，就放进候选池，交给 Python 去精细打分
                    let isMatch = false;
                    if (hintText && elText.includes(hintText)) isMatch = true;
                    if (hintRole && el.getAttribute('role') === hintRole) isMatch = true;
                    if (hintPlaceholder && el.getAttribute('placeholder') === hintPlaceholder) isMatch = true;
                    
                    if (hasHint && !isMatch) {{
                        continue;
                    }}
                    
                    const rect = el.getBoundingClientRect();
                    const style = window.getComputedStyle(el);
                    const visible = rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden';
                    
                    results.push({{
                        index: i,
                        text: elText,
                        visible: visible,
                        role: el.getAttribute('role') || null,
                        placeholder: el.getAttribute('placeholder') || null
                    }});
                    
                    if (results.length >= 50) break;
                }}
                return results;
            }}''', timeout=500)
        except Exception as e:
            logger.debug(f"Failed to batch extract candidate info: {e}")
            return []

        candidates = []
        for data in candidates_data:
            data["locator"] = scope.locator(tag).nth(data["index"])
            candidates.append(data)

        return candidates

    def _score(self, candidate: dict, step: dict) -> float:
        score = 0.0
        hint = step.get("target_hint", {})

        # 1. 文本匹配（最重要）
        hint_text = hint.get("text")
        if hint_text and candidate.get("text"):
            hint_text = hint_text.strip()
            cand_text = candidate["text"].strip()
            if hint_text in cand_text:
                score += 5.0
                # 如果完全相等，给予绝对高分（防止选中包含该文本的父容器）
                if hint_text == cand_text:
                    score += 5.0
                else:
                    # 根据多余文本的长度进行惩罚，多余字符越多，扣分越多（最多扣4分）
                    length_diff = len(cand_text) - len(hint_text)
                    score -= min(4.0, length_diff * 0.05)

        # 2. role 匹配
        hint_role = hint.get("role")
        if hint_role and candidate.get("role") == hint_role:
            score += 3

        # 3. placeholder 匹配
        hint_placeholder = hint.get("placeholder")
        if hint_placeholder and candidate.get("placeholder") == hint_placeholder:
            score += 2

        # 4. 可见性
        if candidate.get("visible"):
            score += 2

        return score

    def _pick_best(self, candidates: List[dict], step: dict) -> Optional[dict]:
        scored = [
            (self._score(c, step), c)
            for c in candidates
        ]
        
        # 过滤掉得分为0的候选（完全不匹配）
        scored = [item for item in scored if item[0] > 0]

        if not scored:
            return None

        scored.sort(reverse=True, key=lambda x: x[0])
        return scored[0][1]

    def _validate(self, locator: Locator) -> bool:
        try:
            # 如果匹配出多个，取第一个来判断可见性，避免严格模式报错
            return locator.count() > 0 and locator.first.is_visible()
        except:
            return False

    def _fallback(self, step: dict) -> Optional[Locator]:
        hint = step.get("target_hint", {})
        if not hint:
            return None
            
        # 兜底等待 8 秒（严格路径已做过 3s 可见性等待）
        timeout_sec = 8.0
        start_time = time.time()
        
        candidates = []
        if hint.get("text"):
            # 如果有 tag 和 text 联合
            if hint.get("tag"):
                # 处理单引号问题
                safe_text = hint["text"].replace('"', '\\"')
                candidates.append(("tag + text", self.page.locator(f'{hint["tag"]}:has-text("{safe_text}")')))
            candidates.append(("exact text", self.page.get_by_text(hint["text"], exact=True)))
            candidates.append(("fuzzy text", self.page.get_by_text(hint["text"], exact=False)))
            
        if hint.get("role"):
            name_arg = hint.get("text")
            if name_arg:
                candidates.append(("role with name", self.page.get_by_role(hint["role"], name=name_arg)))
            candidates.append(("pure role", self.page.get_by_role(hint["role"])))
            
        if hint.get("recorded_selector"):
            selector = hint["recorded_selector"]
            candidates.append(("recorded_selector", self.page.locator(selector)))

        while time.time() - start_time < timeout_sec:
            for strategy, loc in candidates:
                try:
                    if loc.count() > 0:
                        # 找到元素后最好再确认一下它是否真的可用，但这里作为 fallback，只要求在 DOM 树中即可
                        logger.info(f"Fallback succeeded using strategy: '{strategy}'")
                        return loc.first
                except Exception as e:
                    # 如果某个选择器语法错误，将其记录，但不要阻断其他循环
                    logger.debug(f"Fallback strategy '{strategy}' error: {e}")
            time.sleep(0.2)
            
        # 所有 fallback 策略均耗尽，记录当前页面信息以便诊断
        try:
            current_url = self.page.url
            current_title = self.page.title()
            logger.warning(f"Fallback exhausted all {len(candidates)} strategies. Current page: url={current_url}, title={current_title}")
        except Exception:
            logger.warning(f"Fallback exhausted all {len(candidates)} strategies. Unable to read current page info.")
        return None

    def _resolve_locator(self, step: dict) -> Optional[Locator]:
        # 对于不需要目标的操作，直接返回 None
        if step.get("action") in ["navigate", "custom_script", "virtual_navigate", "switch_view"]:
            return None
            
        scopes = self._get_scopes(step)
        
        all_candidates_info = []

        for scope in scopes:
            candidates = self._collect_candidates(scope, step)
            
            if not candidates:
                continue

            best = self._pick_best(candidates, step)
            
            if best:
                all_candidates_info.append(best)
                if self._validate(best["locator"]):
                    self._log_step(step, candidates, best)
                    return best["locator"]
                # 候选元素在 DOM 中存在但不可见，短等一下渲染（避免直接掉入 15s fallback）
                try:
                    best["locator"].first.wait_for(state="visible", timeout=3000)
                    self._log_step(step, candidates, best)
                    return best["locator"]
                except Exception:
                    pass

        # 如果都没通过 validate，记录日志并尝试 Fallback
        self._log_step(step, all_candidates_info, None)
        logger.warning(f"Failed to resolve strict locator for step {step.get('action')}, triggering fallback.")
        return self._fallback(step)

    def _execute_action(self, locator: Optional[Locator], step: dict):
        action = step.get("action")
        value = step.get("value")

        if action == "navigate":
            nav_url = value or step.get("url")
            if nav_url:
                try:
                    self.page.goto(nav_url, wait_until="domcontentloaded", timeout=15000)
                except Exception as e:
                    raise RuntimeError(f"导航失败: {nav_url} - {e}")
                # SPA 组件渲染等待：networkidle 确保异步组件已挂载
                try:
                    self.page.wait_for_load_state("networkidle", timeout=5000)
                except Exception:
                    logger.debug(f"networkidle wait timed out for {nav_url}, continuing anyway")
                self.page.wait_for_timeout(500)
        elif action == "click":
            locator.click(force=True)
        elif action == "fill":
            locator.fill(value or "", force=True)
        elif action == "select":
            locator.select_option(value, force=True)
        elif action == "check":
            locator.check(force=True)
        elif action == "uncheck":
            locator.uncheck(force=True)
        elif action == "hover":
            locator.hover(force=True)
        elif action == "press":
            locator.press(value)
        elif action in ("virtual_navigate", "switch_view"):
            # 录制元数据：SPA 路由切换 / 视图切换已在之前的操作中完成，无需额外执行
            pass
        elif action == "expect_visible":
            from playwright.sync_api import expect
            expect(locator).to_be_visible()
        elif action == "expect_hidden":
            from playwright.sync_api import expect
            expect(locator).to_be_hidden()
        elif action == "expect_text":
            from playwright.sync_api import expect
            tag = ""
            try:
                tag = locator.evaluate("el => el.tagName.toLowerCase()")
            except Exception:
                pass
                
            if tag in ["input", "textarea", "select"]:
                expect(locator).to_have_value(value or "")
            else:
                expect(locator).to_have_text(value or "")

    def _post_validate(self, step: dict):
        action = step.get("action")
        
        # 简单等待策略，留待后续演进
        if action == "click":
            self.page.wait_for_timeout(300)
            
        # 预留可扩展的空间：
        # - URL 变化等待
        # - DOM 稳定等待
        # - Toast 出现等待

    def _log_step(self, step: dict, candidates: list, chosen: Optional[dict]):
        logger.info(f"[Execution Engine] Step: {step.get('action')}, Target Hint: {step.get('target_hint')}")
        logger.info(f"  -> Found {len(candidates)} candidates.")
        if chosen:
            logger.info(f"  -> Chosen element text: '{chosen.get('text')}', role: {chosen.get('role')}")
        else:
            logger.info("  -> No valid candidate chosen in strict scopes.")
