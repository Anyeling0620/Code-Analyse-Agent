import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import type { CurrentStage, GoalType, ProfileForm, SkillLevel } from '../types/chat';

const skillLevels: SkillLevel[] = ['零基础', '入门', '熟悉'];
const goalTypes: GoalType[] = ['补基础', '做项目', '学Agent'];
const currentStages: CurrentStage[] = ['学习中', '开发中', '联调收尾', '复盘中'];

// 描述是要喂给模型的整段背景，太长既占上下文又没人读完，所以前端截断、后端也限制长度。
const descriptionMaxLength = 1000;

type ProfileModalProps = {
  profile: ProfileForm;
  // gate=true 是「登录后的强引导」：不能关闭，六项都填了才能保存进主界面。
  gate?: boolean;
  // onSave 允许是异步的：实现里会 await 它，失败就把错误显示在弹窗里（父组件负责成功时关闭）。
  onSave: (profile: ProfileForm) => void | Promise<void>;
  onClose: () => void;
};

export function ProfileModal({ profile, gate = false, onSave, onClose }: ProfileModalProps) {
  const [draft, setDraft] = useState<ProfileForm>(profile);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  const nextProfile = useMemo<ProfileForm>(() => ({
    user_type: draft.user_type.trim(),
    skill_level: draft.skill_level.trim(),
    goal_type: draft.goal_type.trim(),
    description: draft.description.trim(),
    current_topic: draft.current_topic.trim(),
    current_stage: draft.current_stage.trim(),
  }), [draft]);

  const complete = isComplete(nextProfile);

  // 强引导下点遮罩、点取消都不生效：要退出只有保存这一条路。
  function requestClose() {
    if (!gate && !saving) {
      onClose();
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!complete || saving) {
      return;
    }
    setSaving(true);
    setSaveError('');
    try {
      await onSave(nextProfile);
    } catch (error) {
      // 保存失败就留在弹窗里：强引导下不能带着"以为存上了"的状态进入工作区。
      setSaveError(error instanceof Error && error.message ? error.message : '保存失败，请重试');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="trace-modal-backdrop" role="presentation" onClick={requestClose}>
      <section className="trace-modal profile-modal" role="dialog" aria-modal="true" aria-label="基础画像" onClick={(event) => event.stopPropagation()}>
        <div className="trace-modal-header">
          <div>
            <h3>{gate ? '先完善基础画像' : '基础画像'}</h3>
            <p>保存后会随下一轮对话提交，帮助 Agent 调整解释深度和学习建议。</p>
          </div>
          {!gate && (
            <button className="ghost-button" type="button" onClick={onClose} aria-label="关闭资料弹窗" disabled={saving}>
              ×
            </button>
          )}
        </div>

        <form className="profile-form" onSubmit={handleSubmit}>
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
              <input
                value={draft.skill_level}
                onChange={(event) => setDraft((current) => ({ ...current, skill_level: event.target.value }))}
                list="profile-skill-levels"
                placeholder="例如 零基础 / 入门 / 熟悉，也可自定义"
              />
              <datalist id="profile-skill-levels">
                {skillLevels.map((level) => (
                  <option key={level} value={level} />
                ))}
              </datalist>
            </label>

            <label className="profile-field">
              <span>目标类型</span>
              <input
                value={draft.goal_type}
                onChange={(event) => setDraft((current) => ({ ...current, goal_type: event.target.value }))}
                list="profile-goal-types"
                placeholder="例如 补基础 / 做项目 / 学Agent，也可自定义"
              />
              <datalist id="profile-goal-types">
                {goalTypes.map((goal) => (
                  <option key={goal} value={goal} />
                ))}
              </datalist>
            </label>

            <label className="profile-field">
              <span>当前阶段</span>
              <input
                value={draft.current_stage}
                onChange={(event) => setDraft((current) => ({ ...current, current_stage: event.target.value }))}
                list="profile-current-stages"
                placeholder="例如 学习中 / 开发中 / 复盘中，也可自定义"
              />
              <datalist id="profile-current-stages">
                {currentStages.map((stage) => (
                  <option key={stage} value={stage} />
                ))}
              </datalist>
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
              <span>描述</span>
              <textarea
                value={draft.description}
                onChange={(event) => setDraft((current) => ({ ...current, description: event.target.value }))}
                placeholder="用几句话介绍自己：背景、在做什么、想得到什么帮助。例如：本科在读，学过 Python/Java，正在用 Go + Eino 做 Agent 项目，希望补齐工程化经验。"
                maxLength={descriptionMaxLength}
                rows={4}
              />
              <span className="profile-field-counter">{draft.description.length}/{descriptionMaxLength}</span>
            </label>
          </div>

          <div className="profile-modal-footer">
            {!gate && (
              <button className="ghost-button" type="button" onClick={onClose} disabled={saving}>取消</button>
            )}
            <button className="primary-button" type="submit" disabled={!complete || saving}>
              {saving ? '保存中…' : '保存资料'}
            </button>
          </div>
          {gate && !complete && (
            <p className="profile-gate-hint">六项都填上才能进入（描述写两句也行）。</p>
          )}
          {saveError && (
            <p className="profile-gate-error">保存失败：{saveError}</p>
          )}
        </form>
      </section>
    </div>
  );
}

function isComplete(profile: ProfileForm): boolean {
  return Object.values(profile).every((value) => value.trim() !== '');
}
