import { strict as assert } from 'node:assert';
import { profileForChat, normalizeProfileUpdatedAt } from './sync.ts';
import { initialProfile } from '../constants/profile.ts';

// fetchProfile 失败（failed）或仍在进行（pending）时，绝不能把本地默认画像提交给服务端。
// 服务端会全量覆盖 profiles，这是“用户画像没存档/被默认值覆盖”的直接原因。
assert.equal(profileForChat(initialProfile, 'failed'), null, '画像同步失败时不得提交默认画像');
assert.equal(profileForChat(initialProfile, 'pending'), null, '画像同步完成前不得提交默认画像');

const custom = {
  user_type: '教师',
  skill_level: '专家',
  goal_type: '技术调研',
  description: '正在做课程',
  current_topic: 'Agent',
  current_stage: '开发中',
};
assert.deepEqual(profileForChat(custom, 'ready'), custom, '同步成功后应原样提交当前画像');

// Go 零值时间不能被当成“已保存”。
assert.equal(normalizeProfileUpdatedAt('0001-01-01T00:00:00Z'), '', '零值时间应视为未保存');
assert.equal(normalizeProfileUpdatedAt(''), '', '空时间应视为未保存');
assert.equal(normalizeProfileUpdatedAt(undefined), '', '缺失时间应视为未保存');
const valid = '2026-10-20T00:17:17.665303704+08:00';
assert.equal(normalizeProfileUpdatedAt(valid), valid, '正常时间应保留原值');
assert.equal(normalizeProfileUpdatedAt('not-a-time'), '', '非法时间应视为未保存');

console.log('profile sync tests passed');
