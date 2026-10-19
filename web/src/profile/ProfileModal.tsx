import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import type { CurrentStage, GoalType, ProfileForm, SkillLevel } from '../types/chat';
import { CloseIcon } from '../ui/icons';

const userTypes = ['学生', '开发者', '教师', '研究员', '产品/运营', '其他'];
const skillLevels: SkillLevel[] = ['零基础', '入门', '熟悉', '熟练', '专家'];
const goalTypes: GoalType[] = ['补基础', '做项目', '学Agent', '求职面试', '技术调研', '其他'];
const currentStages: CurrentStage[] = ['学习中', '开发中', '联调收尾', '复盘中', '已上线维护', '规划中'];

// selectOptions 保留旧数据里的自定义值：历史画像可能存着列表外的字符串，
// 直接塞进 <select> 会显示为空白，这里把它补成一个可选项。
function selectOptions(options: readonly string[], current: string): string[] {
  const value = current.trim();
  if (!value || options.includes(value)) {
    return [...options];
  }
  return [...options, value];
}

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
  const filled = Object.values(nextProfile).filter((value) => value.trim() !== '').length;

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
    <div className="modal-backdrop" role="presentation" onClick={requestClose}>
      <section className="modal-card profile-modal" role="dialog" aria-modal="true" aria-label="基础画像" onClick={(event) => event.stopPropagation()}>
        <span className="modal-tab" style={{ background: 'var(--section-violet)', color: 'var(--on-violet)' }} aria-hidden="true">
          {gate ? 'ONBOARDING' : 'PROFILE'}
        </span>

        <header className="modal-header">
          <div>
            <h2>{gate ? '先完善基础画像' : '基础画像'}</h2>
            <p>保存后会随下一轮对话提交，帮助 Agent 调整解释深度和学习建议。</p>
          </div>
          {!gate && (
            <button className="icon-button" type="button" onClick={onClose} aria-label="关闭资料弹窗" disabled={saving}>
              <CloseIcon size={17} />
            </button>
          )}
        </header>

        <form className="profile-form" onSubmit={handleSubmit}>
          <div className="profile-grid">
            <label className="field">
              <span className="field-label">用户类型</span>
              <select
                value={draft.user_type.trim()}
                onChange={(event) => setDraft((current) => ({ ...current, user_type: event.target.value }))}
              >
                {draft.user_type.trim() === '' && <option value="">请选择</option>}
                {selectOptions(userTypes, draft.user_type).map((type) => (
                  <option key={type} value={type}>{type}</option>
                ))}
              </select>
            </label>

            <label className="field">
              <span className="field-label">技能水平</span>
              <select
                value={draft.skill_level.trim()}
                onChange={(event) => setDraft((current) => ({ ...current, skill_level: event.target.value }))}
              >
                {draft.skill_level.trim() === '' && <option value="">请选择</option>}
                {selectOptions(skillLevels, draft.skill_level).map((level) => (
                  <option key={level} value={level}>{level}</option>
                ))}
              </select>
            </label>

            <label className="field">
              <span className="field-label">目标类型</span>
              <select
                value={draft.goal_type.trim()}
                onChange={(event) => setDraft((current) => ({ ...current, goal_type: event.target.value }))}
              >
                {draft.goal_type.trim() === '' && <option value="">请选择</option>}
                {selectOptions(goalTypes, draft.goal_type).map((goal) => (
                  <option key={goal} value={goal}>{goal}</option>
                ))}
              </select>
            </label>

            <label className="field">
              <span className="field-label">当前阶段</span>
              <select
                value={draft.current_stage.trim()}
                onChange={(event) => setDraft((current) => ({ ...current, current_stage: event.target.value }))}
              >
                {draft.current_stage.trim() === '' && <option value="">请选择</option>}
                {selectOptions(currentStages, draft.current_stage).map((stage) => (
                  <option key={stage} value={stage}>{stage}</option>
                ))}
              </select>
            </label>

            <label className="field field-full">
              <span className="field-label">当前主题</span>
              <input
                value={draft.current_topic}
                onChange={(event) => setDraft((current) => ({ ...current, current_topic: event.target.value }))}
                placeholder="例如 Go Agent Eino 实战"
              />
            </label>

            <label className="field field-full">
              <span className="field-label">描述</span>
              <textarea
                value={draft.description}
                onChange={(event) => setDraft((current) => ({ ...current, description: event.target.value }))}
                placeholder="用几句话介绍自己：背景、在做什么、想得到什么帮助。例如：本科在读，学过 Python/Java，正在用 Go + Eino 做 Agent 项目，希望补齐工程化经验。"
                maxLength={descriptionMaxLength}
                rows={4}
              />
              <span className="field-counter">{draft.description.length}/{descriptionMaxLength}</span>
            </label>
          </div>

          <div className="profile-progress">
            <span className="profile-progress-label">已填写</span>
            <span className="profile-progress-value">{filled} / 6</span>
            <span className="profile-progress-track" aria-hidden="true">
              <span style={{ width: `${(filled / 6) * 100}%` }} />
            </span>
          </div>

          <footer className="modal-footer">
            {!gate && (
              <button className="ghost-button" type="button" onClick={onClose} disabled={saving}>取消</button>
            )}
            <button className="primary-button" type="submit" disabled={!complete || saving}>
              {saving ? '保存中…' : '保存资料'}
            </button>
          </footer>

          {gate && !complete && (
            <p className="form-hint">六项都填上才能进入（描述写两句也行）。</p>
          )}
          {saveError && (
            <p className="form-error" role="alert">保存失败：{saveError}</p>
          )}
        </form>
      </section>
    </div>
  );
}

function isComplete(profile: ProfileForm): boolean {
  return Object.values(profile).every((value) => value.trim() !== '');
}
