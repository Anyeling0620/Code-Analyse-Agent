import type { ProfileForm } from '../types/chat';

export const initialProfile: ProfileForm = {
  user_type: '学生',
  skill_level: '入门',
  goal_type: '学Agent',
  // 描述是必填项，但很多人没有额外背景可写；默认「无」，避免卡住强引导。
  description: '无',
  current_topic: 'Go Agent Eino 实战',
  current_stage: '学习中',
};
