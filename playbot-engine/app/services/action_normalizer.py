import logging
from typing import List, Dict, Any
from app.models.semantic_ir import SemanticStep, ActionType, TargetHint

log = logging.getLogger(__name__)

class ActionNormalizer:
    @staticmethod
    def _extract_selector(raw: dict) -> str:
        attrs = raw.get('attrs', {})
        if 'data-testid' in attrs:
            return f"[data-testid='{attrs['data-testid']}']"
        if 'id' in attrs:
            return f"#{attrs['id']}"
        if 'name' in attrs:
            return f"[name='{attrs['name']}']"
        return raw.get('path', '')

    @staticmethod
    def normalize(actions: list[dict]) -> List[SemanticStep]:
        """
        Takes raw action_history items and returns a list of SemanticSteps.
        """
        raw_steps = []
        network_events = []
        
        # 1. 基础分发与第一轮分类提取
        i = 0
        while i < len(actions):
            act = actions[i]
            raw = act.get('raw_data', {})
            act_type = raw.get('action') 
            url = act.get('url') or raw.get('url', '')
            
            # 分离出网络事件池（不独立成步骤，稍后挂载到关联动作上）
            if act_type == 'network_response':
                network_events.append({
                    "time": act.get("time", 0),
                    "data": {
                        "request": raw.get("url"),
                        "method": raw.get("method"),
                        "status": raw.get("status")
                    }
                })
                i += 1
                continue
                
            target_hint = TargetHint(
                tag=raw.get('tag', 'unknown'),
                text=raw.get('text', ''),
                role=raw.get('attrs', {}).get('role'),
                placeholder=raw.get('attrs', {}).get('placeholder'),
                dom_fragment=raw.get('dom_fragment', ''),
                recorded_selector=ActionNormalizer._extract_selector(raw)
            )
            
            target_component = raw.get('component')
            
            if act_type == 'input':
                final_val = raw.get('value', '')
                j = i + 1
                while j < len(actions):
                    next_act = actions[j]
                    if next_act.get('raw_data', {}).get('action') == 'input' and next_act.get('raw_data', {}).get('path') == raw.get('path'):
                        final_val = next_act.get('raw_data', {}).get('value', '')
                        j += 1
                    else: break
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.FILL, target_hint=target_hint, target_component=target_component, url=url, value=final_val)))
                i = j - 1
                
            elif act_type == 'click':
                # 去重点击
                j = i + 1
                while j < len(actions) and actions[j].get('raw_data', {}).get('action') == 'click' and actions[j].get('raw_data', {}).get('path') == raw.get('path'):
                    j += 1
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.CLICK, target_hint=target_hint, target_component=target_component, url=url)))
                i = j - 1
                
            elif act_type == 'keydown':
                if raw.get('value') == 'Enter':
                    # 提纯出表单提交的意图
                    raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.SUBMIT_FORM, target_hint=target_hint, target_component=target_component, url=url)))
                
            elif act_type == 'hover':
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.HOVER, target_hint=target_hint, target_component=target_component, url=url)))
                
            elif act_type == 'virtual_navigate':
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.VIRTUAL_NAVIGATE, url=url)))
                
            elif act_type == 'title_changed':
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.SWITCH_VIEW, url=url, value=raw.get('value'))))
                
            elif act_type == 'handle_dialog':
                dialog_target_hint = TargetHint(tag='dialog', text=raw.get('value'))
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.HANDLE_DIALOG, url=url, value=raw.get('value'), target_hint=dialog_target_hint)))
                
            elif act_type == 'upload_file':
                raw_steps.append((act.get('time', 0), SemanticStep(action=ActionType.UPLOAD_FILE, url=url)))
                
            i += 1

        # 2. 复合动作推断 (Complex Component Inference) - 例如将连贯的 Click 折叠为 Select
        refined_steps = []
        i = 0
        while i < len(raw_steps):
            t1, step = raw_steps[i]
            if step.action == ActionType.CLICK:
                # 检查后面一小堆时间内是否有另一个点击
                j = i + 1
                found_composite = False
                while j < len(raw_steps) and j < i + 3:
                    t2, next_step = raw_steps[j]
                    if next_step.action == ActionType.CLICK and (t2 - t1) < 2.5: # 2.5秒内的二次点击
                        # 判断是否是从父容器点击菜单项 (典型的下拉框 / Popup)
                        is_portal = next_step.target_hint and ('li' in next_step.target_hint.tag or 'option' in next_step.target_hint.tag or (next_step.target_hint.recorded_selector and 'menu' in next_step.target_hint.recorded_selector))
                        is_combobox = step.target_hint and (step.target_hint.role in ['combobox', 'button'])
                        
                        if is_portal or is_combobox:
                            # 折叠为 SELECT
                            step.action = ActionType.SELECT
                            step.value = next_step.target_hint.text if next_step.target_hint else ""
                            i = j # 吞并第二个点击
                            found_composite = True
                            break
                    j += 1
            refined_steps.append((t1, step))
            i += 1
            
        # 3. 挂载网络关联 (Network Topology Binding) & 导航计算
        final_steps = []
        last_url = None
        for timestamp, step in refined_steps:
            # Inject native navigation
            if step.url and step.url != last_url and step.action not in [ActionType.VIRTUAL_NAVIGATE, ActionType.SWITCH_VIEW]:
                final_steps.append(SemanticStep(action=ActionType.NAVIGATE, url=step.url))
                last_url = step.url
                
            if step.action in [ActionType.VIRTUAL_NAVIGATE, ActionType.SWITCH_VIEW]:
                last_url = step.url

            # 寻找动作后 1.5 秒内发生的网络请求，挂靠为 action 的 consequence
            relevant_networks = [n for n in network_events if timestamp < n["time"] <= timestamp + 1.5]
            if relevant_networks:
                # 取最有代表性的一个（通常是最早触发或者耗时最久的，此处简单取第一个有效数据请求）
                step.network = relevant_networks[0]["data"]
                
            final_steps.append(step)

        return final_steps
