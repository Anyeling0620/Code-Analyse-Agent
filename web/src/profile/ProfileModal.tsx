import { useMemo, useState } from 'react';
import type { CurrentStage, GoalType, ProfileForm, SkillLevel } from '../types/chat';

const skillLevels: SkillLevel[] = ['零基础', '入门', '熟悉'];
const goalTypes: GoalType[] = ['补基础', '做项目', '学Agent'];
const currentStages: CurrentStage[] = ['学习中', '开发中', '联调收尾', '复盘中'];

type ProfileModalProps = {
  profile: ProfileForm;
  onSave: (profile: ProfileForm) => void;
  onClose: () => void;
};

export function ProfileModal({ profile, onSave, onClose }: ProfileModalProps) {
  const [draft, setDraft] = useState<ProfileForm>(profile);
  const [coursesText, setCoursesText] = useState(() => profile.purchased_courses.join('\n'));

  const nextProfile = useMemo<ProfileForm>(() => ({
    user_type: draft.user_type.trim(),
    skill_level: draft.skill_level,
    goal_type: draft.goal_type,
    purchased_courses: parseCourses(coursesText),
    current_topic: draft.current_topic.trim(),
    current_stage: draft.current_stage,
  }), [coursesText, draft]);

  return (
    <div className="trace-modal-backdrop" role="presentation" onClick={onClose}>
      <section className="trace-modal profile-modal" role="dialog" aria-modal="true" aria-label="基础画像" onClick={(event) => event.stopPropagation()}>
        <div className="trace-modal-header">
          <div>
            <h3>基础画像</h3>
            <p>保存后会随下一轮对话提交，帮助 Agent 调整解释深度和学习建议。</p>
          </div>
          <button className="ghost-button" type="button" onClick={onClose} aria-label="关闭资料弹窗">
            ×
          </button>
        </div>

        <form
          className="profile-form"
          onSubmit={(event) => {
            event.preventDefault();
            onSave(nextProfile);
          }}
        >
          <div className="profile-form-grid">
            <label className="profile-field">
              <span>用户类型</span>
              <input
                value={draft.user_type}
                onChange={(event) => setDraft((current) => ({ ...current, user_type: event.target.value }))}
                placeholder="例如 student / developer"
              />
            </label>

            <label className="profile-field">
              <span>技能水平</span>
              <select value={draft.skill_level} onChange={(event) => setDraft((current) => ({ ...current, skill_level: event.target.value as SkillLevel }))}>
                {skillLevels.map((level) => (
                  <option key={level} value={level}>{level}</option>
                ))}
              </select>
            </label>

            <label className="profile-field">
              <span>目标类型</span>
              <select value={draft.goal_type} onChange={(event) => setDraft((current) => ({ ...current, goal_type: event.target.value as GoalType }))}>
                {goalTypes.map((goal) => (
                  <option key={goal} value={goal}>{goal}</option>
                ))}
              </select>
            </label>

            <label className="profile-field">
              <span>当前阶段</span>
              <select value={draft.current_stage} onChange={(event) => setDraft((current) => ({ ...current, current_stage: event.target.value as CurrentStage }))}>
                {currentStages.map((stage) => (
                  <option key={stage} value={stage}>{stage}</option>
                ))}
              </select>
            </label>

            <label className="profile-field profile-field-full">
              <span>当前主题</span>
              <input
                value={draft.current_topic}
                onChange={(event) => setDraft((current) => ({ ...current, current_topic: event.target.value }))}
                placeholder="例如 Go Agent Eino 实战"
              />
            </label>

            <label className="profile-field profile-field-full">
              <span>已购课程</span>
              <textarea
                value={coursesText}
                onChange={(event) => setCoursesText(event.target.value)}
                placeholder="每行一个课程，或用逗号、分号分隔"
              />
            </label>
          </div>

          <div className="profile-modal-footer">
            <button className="ghost-button" type="button" onClick={onClose}>取消</button>
            <button className="primary-button" type="submit">保存资料</button>
          </div>
        </form>
      </section>
    </div>
  );
}

function parseCourses(value: string): string[] {
  const seen = new Set<string>();
  const courses: string[] = [];
  for (const item of value.split(/[\n,，;；]+/)) {
    const course = item.trim();
    if (!course || seen.has(course)) {
      continue;
    }
    seen.add(course);
    courses.push(course);
  }
  return courses;
}
