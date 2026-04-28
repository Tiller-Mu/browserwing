"""
代码静态分析服务 - 使用AST提取源码关键信息
"""
import ast
import re
from typing import Dict, List, Any, Optional


class VueCodeAnalyzer:
    """Vue文件分析器"""
    
    @staticmethod
    def extract_script_content(vue_code: str) -> str:
        """提取Vue文件中的<script>内容"""
        # 匹配 <script setup> 或 <script>
        match = re.search(r'<script[^>]*>(.*?)</script>', vue_code, re.DOTALL)
        if match:
            return match.group(1).strip()
        return vue_code  # 如果不是Vue文件，返回原内容
    
    @staticmethod
    def analyze_javascript(script_code: str) -> Dict[str, Any]:
        """分析JavaScript/TypeScript代码，提取关键信息"""
        result = {
            'imports': [],
            'data_vars': [],
            'methods': [],
            'computed': [],
            'watchers': [],
            'lifecycle_hooks': [],
            'event_handlers': [],
            'refs': []
        }
        
        # 提取import语句
        import_pattern = r"import\s+(?:{([^}]+)}|(\w+))\s+from\s+['\"]([^'\"]+)['\"]"
        for match in re.finditer(import_pattern, script_code):
            if match.group(1):  # 解构导入
                items = [item.strip() for item in match.group(1).split(',')]
                result['imports'].extend(items)
            elif match.group(2):  # 默认导入
                result['imports'].append(match.group(2))
        
        # 提取ref/reactive变量
        ref_pattern = r'(?:const|let|var)\s+(\w+)\s*=\s*(?:ref|reactive|computed)\s*\('
        for match in re.finditer(ref_pattern, script_code):
            result['refs'].append(match.group(1))
        
        # 提取函数定义
        func_pattern = r'(?:const|function)\s+(\w+)\s*(?:=\s*)?\([^)]*\)\s*(?:=>)?\s*{'
        for match in re.finditer(func_pattern, script_code):
            func_name = match.group(1)
            if func_name.startswith('on'):
                result['lifecycle_hooks'].append(func_name)
            elif func_name.startswith('handle'):
                result['event_handlers'].append(func_name)
            else:
                result['methods'].append(func_name)
        
        # 提取computed
        computed_pattern = r'computed\s*\(\s*\(\s*\)\s*=>\s*{'
        if re.search(computed_pattern, script_code):
            # 尝试提取computed变量名
            computed_var_pattern = r'(?:const|let|var)\s+(\w+)\s*=\s*computed\s*\('
            for match in re.finditer(computed_var_pattern, script_code):
                result['computed'].append(match.group(1))
        
        # 提取watch
        watch_pattern = r'watch\s*\(\s*([^,)]+)'
        for match in re.finditer(watch_pattern, script_code):
            result['watchers'].append(match.group(1).strip())
        
        return result
    
    @staticmethod
    def extract_template_info(vue_code: str) -> Dict[str, Any]:
        """提取模板中的关键信息"""
        result = {
            'events': [],
            'bindings': [],
            'conditionals': [],
            'loops': [],
            'components': []
        }
        
        # 提取事件绑定 @click="handler"
        event_pattern = r'@(\w+)\s*=\s*["\']([^"\']+)["\']'
        for match in re.finditer(event_pattern, vue_code):
            result['events'].append({
                'event': match.group(1),
                'handler': match.group(2)
            })
        
        # 提取v-model绑定
        vmodel_pattern = r'v-model\s*=\s*["\']([^"\']+)["\']'
        for match in re.finditer(vmodel_pattern, vue_code):
            result['bindings'].append({'type': 'v-model', 'var': match.group(1)})
        
        # 提取v-if/v-show
        conditional_pattern = r'v-(if|show)\s*=\s*["\']([^"\']+)["\']'
        for match in re.finditer(conditional_pattern, vue_code):
            result['conditionals'].append({
                'type': match.group(1),
                'condition': match.group(2)
            })
        
        # 提取v-for
        vfor_pattern = r'v-for\s*=\s*["\']([^"\']+)["\']'
        for match in re.finditer(vfor_pattern, vue_code):
            result['loops'].append(match.group(1))
        
        # 提取使用的组件
        component_pattern = r'<([A-Z][a-zA-Z0-9]*)'
        for match in re.finditer(component_pattern, vue_code):
            result['components'].append(match.group(1))
        
        return result
    
    @classmethod
    def analyze(cls, vue_code: str) -> Dict[str, Any]:
        """完整分析Vue文件"""
        script = cls.extract_script_content(vue_code)
        script_info = cls.analyze_javascript(script)
        template_info = cls.extract_template_info(vue_code)
        
        return {
            'script': script_info,
            'template': template_info,
            'summary': cls._generate_summary(script_info, template_info)
        }
    
    @staticmethod
    def _generate_summary(script: Dict, template: Dict) -> str:
        """生成可读性摘要"""
        parts = []
        
        if script['refs']:
            parts.append(f"响应式变量: {', '.join(script['refs'][:5])}")
        if script['methods']:
            parts.append(f"方法: {', '.join(script['methods'][:5])}")
        if script['event_handlers']:
            parts.append(f"事件处理: {', '.join(script['event_handlers'][:3])}")
        if template['events']:
            events = [e['event'] for e in template['events'][:3]]
            parts.append(f"绑定事件: {', '.join(events)}")
        if template['components']:
            comps = list(set(template['components']))[:5]
            parts.append(f"使用组件: {', '.join(comps)}")
        
        return '; '.join(parts) if parts else '简单页面'


class DOMAnalyzer:
    """DOM数据分析器"""
    
    @staticmethod
    def extract_interactive_elements(dom_data: Any) -> List[Dict[str, Any]]:
        """提取交互元素，精简关键信息"""
        elements = []
        
        if isinstance(dom_data, dict):
            # 如果是analyze_page返回的结构
            interactive = dom_data.get('interactive_elements', [])
        elif isinstance(dom_data, list):
            interactive = dom_data
        else:
            return elements
        
        for elem in interactive[:20]:  # 限制数量
            info = {
                'tag': elem.get('tag', 'unknown'),
                'type': elem.get('type', ''),
                'id': elem.get('id', ''),
                'class': elem.get('class', ''),
                'text': elem.get('text', '')[:30] if elem.get('text') else '',
                'selector': elem.get('selector', ''),
                'events': elem.get('events', [])
            }
            elements.append({k: v for k, v in info.items() if v})
        
        return elements
    
    @staticmethod
    def extract_forms(dom_data: Any) -> List[Dict[str, Any]]:
        """提取表单结构"""
        forms = []
        
        if isinstance(dom_data, dict):
            interactive = dom_data.get('interactive_elements', [])
        elif isinstance(dom_data, list):
            interactive = dom_data
        else:
            return forms
        
        current_form = None
        for elem in interactive:
            tag = elem.get('tag', '').lower()
            
            if tag == 'form':
                current_form = {
                    'id': elem.get('id', ''),
                    'action': elem.get('attributes', {}).get('action', ''),
                    'fields': []
                }
                forms.append(current_form)
            elif tag in ['input', 'select', 'textarea'] and current_form:
                current_form['fields'].append({
                    'type': elem.get('type', tag),
                    'name': elem.get('name', ''),
                    'id': elem.get('id', ''),
                    'required': elem.get('required', False)
                })
        
        return forms


def analyze_page_data(source_code: str, dom_data: Any, file_path: str = '') -> Dict[str, Any]:
    """
    综合分析页面数据，提取关键信息供LLM使用
    
    Args:
        source_code: 页面源码
        dom_data: DOM数据
        file_path: 文件路径（用于判断文件类型）
    
    Returns:
        结构化分析结果
    """
    is_vue = file_path.endswith('.vue') or '<template>' in source_code or '<script' in source_code
    result = {
        'file_type': 'vue' if is_vue else 'unknown',
        'code_structure': {},
        'dom_structure': {}
    }
    
    # 分析源码
    if source_code:
        if result['file_type'] == 'vue':
            vue_analysis = VueCodeAnalyzer.analyze(source_code)
            result['code_structure'] = {
                'refs': vue_analysis['script']['refs'][:10],
                'methods': vue_analysis['script']['methods'][:10],
                'event_handlers': vue_analysis['script']['event_handlers'][:5],
                'lifecycle_hooks': vue_analysis['script']['lifecycle_hooks'][:5],
                'template_events': vue_analysis['template']['events'][:10],
                'components': list(set(vue_analysis['template']['components']))[:10]
            }
    
    # 分析DOM
    if dom_data:
        interactive = DOMAnalyzer.extract_interactive_elements(dom_data)
        forms = DOMAnalyzer.extract_forms(dom_data)
        
        result['dom_structure'] = {
            'interactive_count': len(interactive),
            'interactive_elements': interactive,
            'forms': forms
        }
    
    return result


def format_for_llm(analysis: Dict[str, Any]) -> str:
    """将分析结果格式化为LLM友好的文本"""
    lines = []
    
    # 代码结构
    if analysis['code_structure']:
        lines.append("【代码结构】")
        
        refs = analysis['code_structure'].get('refs', [])
        if refs:
            lines.append(f"  响应式变量: {', '.join(refs)}")
        
        methods = analysis['code_structure'].get('methods', [])
        if methods:
            lines.append(f"  方法: {', '.join(methods)}")
        
        handlers = analysis['code_structure'].get('event_handlers', [])
        if handlers:
            lines.append(f"  事件处理函数: {', '.join(handlers)}")
        
        hooks = analysis['code_structure'].get('lifecycle_hooks', [])
        if hooks:
            lines.append(f"  生命周期钩子: {', '.join(hooks)}")
        
        events = analysis['code_structure'].get('template_events', [])
        if events:
            lines.append(f"  模板绑定事件: {', '.join(list(set(events)))}")
        
        components = analysis['code_structure'].get('components', [])
        if components:
            lines.append(f"  使用的组件: {', '.join(components)}")
    
    # DOM结构
    if analysis['dom_structure']:
        lines.append("\n【页面元素】")
        count = analysis['dom_structure'].get('interactive_count', 0)
        lines.append(f"  交互元素数量: {count}")
        
        elements = analysis['dom_structure'].get('interactive_elements', [])
        if elements:
            lines.append("  主要交互元素:")
            for elem in elements[:8]:
                tag = elem.get('tag', '')
                elem_id = elem.get('id', '')
                elem_class = elem.get('class', '')
                text = elem.get('text', '')[:20]
                events = ', '.join(elem.get('events', [])[:2])
                
                parts = [f"    - {tag}"]
                if elem_id:
                    parts.append(f"id={elem_id}")
                if elem_class:
                    parts.append(f"class={elem_class[:20]}")
                if text:
                    parts.append(f"text='{text}'")
                if events:
                    parts.append(f"events=[{events}]")
                
                lines.append(' '.join(parts))
        
        forms = analysis['dom_structure'].get('forms', [])
        if forms:
            lines.append(f"\n  表单数量: {len(forms)}")
            for i, form in enumerate(forms[:3], 1):
                form_id = form.get('id', f'form_{i}')
                fields = form.get('fields', [])
                field_names = [f.get('name', f.get('id', '?')) for f in fields[:5]]
                lines.append(f"    表单 {form_id}: 字段={', '.join(field_names)}")
    
    return '\n'.join(lines)
