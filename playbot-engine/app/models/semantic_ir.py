from enum import Enum
from typing import List, Optional, Dict, Any, Literal
from pydantic import BaseModel, Field

class ActionType(str, Enum):
    # 原 semantic_ir
    NAVIGATE = "navigate"
    VIRTUAL_NAVIGATE = "virtual_navigate"  # SPA 无刷新跳转
    SWITCH_VIEW = "switch_view"  # 大面积DOM更换的视图切换
    CLICK = "click"
    FILL = "fill"
    SELECT = "select"  # 经提纯后的下拉框综合选择
    SUBMIT_FORM = "submit_form"
    HOVER = "hover"  # 引发DOM突变的有效悬停
    HANDLE_DIALOG = "handle_dialog"  # 处理原生弹窗
    UPLOAD_FILE = "upload_file"  # 文件上传
    
    # 扩展（原来 test_schema 补充的动作）
    PRESS = "press"
    CHECK = "check"
    UNCHECK = "uncheck"
    CLEAR = "clear"
    CUSTOM_SCRIPT = "custom_script"
    
    # 断言相关
    ASSERT_VISIBLE = "expect_visible" # 修正为 expect_visible，兼容现有执行引擎
    EXPECT_VISIBLE = "expect_visible"
    EXPECT_HIDDEN = "expect_hidden"
    EXPECT_TEXT = "expect_text"
    EXPECT_ENABLED = "expect_enabled"
    EXPECT_DISABLED = "expect_disabled"

class TargetHint(BaseModel):
    text: Optional[str] = Field(None, description="元素的可见文本内容")
    tag: Optional[str] = Field(None, description="元素的HTML标签，如 button, input")
    role: Optional[str] = Field(None, description="元素的ARIA role，如 button, textbox")
    placeholder: Optional[str] = Field(None, description="输入框的 placeholder 属性")
    dom_fragment: Optional[str] = Field(None, description="局部 HTML 上下文片段，供大模型理解DOM结构")
    recorded_selector: Optional[str] = Field(None, description="从录制轨迹中提取的真实 CSS Selector 或 XPath，用作强兜底")

class ContextHint(BaseModel):
    parent: Optional[str] = Field(None, description="父级容器特征，如 form")
    section: Optional[str] = Field(None, description="所处区块，如 header, footer")

class SemanticStep(BaseModel):
    action: ActionType = Field(..., description="纯语义动作类型，如 click, fill, expect_visible 等。")
    target_hint: Optional[TargetHint] = Field(default_factory=TargetHint, description="目标元素的属性提示，用于运行时评分寻址")
    target_component: Optional[str] = Field(None, description="目标所在的组件文件路径，用于划定沙盒作用域")
    context_hint: Optional[ContextHint] = Field(None, description="目标所在的结构上下文")
    value: Optional[str] = Field(None, description="操作参数：navigate 动作为目标 URL，fill 为输入文本，select 为选项值，expect_text 为预期文本。无操作参数时留空。")
    intent_reason: str = Field(default="", description="这步操作的理由，为什么要这么做（比如：'用于触发邮箱格式校验报错'）")
    
    # 录制元数据（非导航目标！导航 URL 请用 value 字段）
    url: Optional[str] = Field(None, description="动作发生时的页面 URL（录制元数据，仅用于追踪和调试。导航目标 URL 必须填入 value 字段！）")
    dom_snapshot_id: Optional[str] = Field(None, description="此动作发生后的 DOM 快照参照ID")
    network: Optional[Dict[str, Any]] = Field(None, description="动作引发的关键 XHR/Fetch 请求状态")
    after_dom_changed: bool = Field(False, description="标记此动作是否引起了页面的局部或整体重绘")

# 兼容过渡（如有老代码仍引用 SemanticAction）
SemanticAction = SemanticStep

class IntentPlan(BaseModel):
    intent: str = Field(default="Automated cross-page intent", description="Natural language description of intent")
    steps: List[SemanticStep]
    assertions: List[SemanticStep] = Field(default_factory=list)
    
    # 覆盖率相关数据
    recorded_components: List[str] = Field(default_factory=list, description="本次运行真实捕获渲染出来的组件列表")
    missed_components: List[str] = Field(default_factory=list, description="静态源码有引用，但本次未渲染执行到的遗漏组件列表")

class TestCasePlan(BaseModel):
    title: str = Field(..., description="用例标题，如 '正确填写所有项并成功保存'")
    description: str = Field(..., description="用例概要，说明用例主要测试的业务点、前置条件与验证目标。")
    steps: List[SemanticStep] = Field(..., description="实现此用例的交互步骤列表")

# 为了兼容老代码中的名字
TestPlanCase = TestCasePlan

class TestPlanBlueprint(BaseModel):
    page_summary: str = Field(..., description="对当前页面功能和所提取轨迹的简明总结")
    test_cases: List[TestCasePlan] = Field(..., description="由模型规划的全部测试用例大纲列表，包含所有的正向和各种异常边界流")
