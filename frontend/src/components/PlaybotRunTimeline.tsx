import { AlertTriangle, Bot, CheckCircle2, CircleDashed, FileCheck2, ListChecks, MessageSquare, XCircle } from 'lucide-react';
import type { PlaybotRunEvent } from '../api/project';

interface PlaybotRunTimelineProps {
  events: PlaybotRunEvent[];
  running: boolean;
}

const phaseLabels: Record<string, string> = {
  queued: '准备生成',
  job_read: '读取任务',
  recording_quality: '校验录制',
  llm_request: '请求模型',
  llm_visible_output: '模型返回',
  compile_blueprint: '编译用例',
  agent_failed: '智能体返回失败',
  agent_done: '智能体处理完成',
  done: '生成完成',
  failed: '生成失败',
  agent_event_stream_invalid: '事件流告警',
};

export default function PlaybotRunTimeline({ events, running }: PlaybotRunTimelineProps) {
  if (events.length === 0 && !running) {
    return null;
  }

  return (
    <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/50 overflow-hidden">
      <div className="px-3 py-2 border-b border-gray-200 dark:border-gray-700 flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-200">
        <Bot className="w-4 h-4 text-gray-500" />
        生成过程
      </div>
      <div className="max-h-72 overflow-y-auto p-3 space-y-3">
        {events.map((event) => (
          <div key={event.seq} className="flex items-start gap-2.5">
            <EventIcon event={event} running={running} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium text-gray-800 dark:text-gray-100">
                  {phaseLabels[event.phase] || event.phase}
                </span>
                {event.level === 'warning' && (
                  <span className="text-[11px] px-1.5 py-0.5 rounded bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200">告警</span>
                )}
                {event.level === 'error' && (
                  <span className="text-[11px] px-1.5 py-0.5 rounded bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-200">失败</span>
                )}
              </div>
              {event.visible_message && (
                <p className="mt-1 text-sm text-gray-700 dark:text-gray-300 whitespace-pre-wrap break-words">
                  {event.visible_message}
                </p>
              )}
              {!event.visible_message && event.message && (
                <p className="mt-1 text-xs text-gray-500 dark:text-gray-400 break-words">{event.message}</p>
              )}
              <CandidateSteps data={event.data} />
              <TextList title="生成说明" values={event.data?.assumptions} />
              <TextList title="风险提示" values={event.data?.risk_notes} />
            </div>
          </div>
        ))}
        {running && events.every((event) => event.phase !== 'done' && event.phase !== 'failed') && (
          <div className="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
            <CircleDashed className="w-4 h-4 animate-spin" />
            等待下一步结果
          </div>
        )}
      </div>
    </div>
  );
}

function EventIcon({ event, running }: { event: PlaybotRunEvent; running: boolean }) {
  if (event.phase === 'done') return <CheckCircle2 className="w-4 h-4 mt-0.5 text-emerald-500 shrink-0" />;
  if (event.phase === 'failed' || event.level === 'error') return <XCircle className="w-4 h-4 mt-0.5 text-red-500 shrink-0" />;
  if (event.level === 'warning') return <AlertTriangle className="w-4 h-4 mt-0.5 text-amber-500 shrink-0" />;
  if (event.phase === 'llm_visible_output') return <MessageSquare className="w-4 h-4 mt-0.5 text-indigo-500 shrink-0" />;
  if (event.phase === 'compile_blueprint') return <FileCheck2 className="w-4 h-4 mt-0.5 text-gray-500 shrink-0" />;
  if (event.phase === 'recording_quality') return <ListChecks className="w-4 h-4 mt-0.5 text-gray-500 shrink-0" />;
  return <CircleDashed className={`w-4 h-4 mt-0.5 text-gray-400 shrink-0 ${running ? 'animate-spin' : ''}`} />;
}

function CandidateSteps({ data }: { data?: Record<string, any> }) {
  const steps = Array.isArray(data?.candidate_steps) ? data?.candidate_steps : [];
  if (steps.length === 0) return null;
  return (
    <div className="mt-2 space-y-1.5">
      <div className="text-xs font-medium text-gray-500 dark:text-gray-400">候选步骤</div>
      {steps.slice(0, 8).map((step: any, index: number) => (
        <div key={`${step.action || 'step'}-${index}`} className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-2">
          <div className="text-xs text-gray-800 dark:text-gray-200">
            <span className="font-medium">{index + 1}. {step.action || 'step'}</span>
            {step.target_summary && <span className="text-gray-500 dark:text-gray-400"> · {step.target_summary}</span>}
          </div>
          {step.value_summary && (
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">{step.value_summary}</div>
          )}
          {step.reason && (
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 line-clamp-2">{step.reason}</div>
          )}
        </div>
      ))}
    </div>
  );
}

function TextList({ title, values }: { title: string; values?: any }) {
  if (!Array.isArray(values) || values.length === 0) return null;
  return (
    <div className="mt-2">
      <div className="text-xs font-medium text-gray-500 dark:text-gray-400">{title}</div>
      <div className="mt-1 space-y-1">
        {values.slice(0, 4).map((value: any, index: number) => (
          <div key={`${title}-${index}`} className="text-xs text-gray-600 dark:text-gray-300 break-words">
            {String(value)}
          </div>
        ))}
      </div>
    </div>
  );
}
