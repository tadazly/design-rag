import type { SectionType } from "../shared/contracts.js";
import { normalizeText } from "./text.js";

const SECTION_RULES: Array<{ type: SectionType; pattern: RegExp }> = [
  { type: "version_history", pattern: /(版本|修订|修改记录|更新记录|历史改动|变更|迭代|复用记录|revision|changelog)/i },
  { type: "animation_requirement", pattern: /(动画|动效|特效需求|动画需求)/i },
  { type: "art_requirement", pattern: /(美术|原画|资源需求|音效|ui需求)/i },
  { type: "statistics", pattern: /(统计|埋点|数据上报|行为分析)/i },
  { type: "reward_value", pattern: /(奖励数值|奖励配置|奖励和消耗|奖品价值|价值表)/i },
  { type: "panel_logic", pattern: /(面板.*逻辑|界面逻辑|panel|页面逻辑)/i },
  { type: "flow", pattern: /(流程|步骤|交互|逻辑|入口|界面流转|状态机|时序)/i },
  { type: "gameplay", pattern: /(玩法|规则|奖励|概率|抽奖|轮盘|转盘|奖池|保底|次数|积分|活动)/i },
  { type: "config", pattern: /(配置|配表|字段|参数|数值|id\b|枚举|掉落|activity|module|dropunit)/i },
  { type: "overview", pattern: /(概述|背景|目标|需求说明|简介|总览|summary|overview)/i },
];

export function classifySection(headingPath: string[], text: string): SectionType {
  const heading = normalizeText(headingPath.join(" / "));
  if (heading) {
    for (const rule of SECTION_RULES) {
      if (rule.pattern.test(heading)) return rule.type;
    }
  }
  const sample = normalizeText(text.slice(0, 240));
  for (const rule of SECTION_RULES) {
    if (rule.pattern.test(sample)) return rule.type;
  }
  return "other";
}

const SYNONYM_GROUPS = [
  ["轮盘", "转盘", "幸运轮盘", "抽奖轮盘", "roulette"],
  ["抽奖", "抽取", "奖池", "概率", "保底", "扭蛋"],
  ["签到", "登录奖励", "每日登录", "累签", "补签"],
  ["复用", "沿用", "套用", "模板", "通用版"],
  ["历史改动", "版本记录", "修改记录", "更新记录", "迭代记录", "变更记录"],
  ["配置", "配表", "字段", "参数", "数值"],
  ["流程", "步骤", "交互", "逻辑", "时序"],
  ["玩法", "规则", "机制"],
  ["奖励", "奖品", "掉落", "兑换"],
];

export function expandQueryTerms(query: string, enabled = true): string[] {
  const normalized = normalizeText(query);
  const terms = new Set<string>([normalized]);
  for (const word of normalized.split(/[\s,，。！？、;；:：]+/)) {
    if (word.length >= 2) terms.add(word);
  }
  if (enabled) {
    for (const group of SYNONYM_GROUPS) {
      if (group.some((term) => normalized.includes(normalizeText(term)))) {
        group.forEach((term) => terms.add(normalizeText(term)));
      }
    }
  }
  return [...terms].filter(Boolean);
}

export function queryConceptGroups(query: string): string[][] {
  const normalized = normalizeText(query);
  return SYNONYM_GROUPS
    .filter((group) => group.some((term) => normalized.includes(normalizeText(term))))
    .map((group) => group.map(normalizeText));
}

export function makeFamilyKey(title: string): { key: string; confidence: number } {
  let key = normalizeText(title)
    .replace(/【\s*(复用|通用|历史|旧版|最终版?)\s*】/g, " ")
    .replace(/[（(]\s*(复用|通用|历史|旧版|最终版?)\s*[)）]/g, " ")
    .replace(/\b(v|ver|version)\s*\d+(?:\.\d+){0,3}\b/gi, " ")
    .replace(/20\d{2}[-_.年/]?\d{1,2}(?:[-_.月/]?\d{1,2}日?)?/g, " ")
    .replace(/[_\-—]+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  const beforeGeneric = key;
  key = key.replace(/(策划案|需求文档|配置表|活动方案|说明文档)$/g, "").trim();
  if (!key) key = normalizeText(title);
  const removed = Math.max(0, normalizeText(title).length - key.length);
  const confidence = Math.max(0.45, Math.min(0.95, 0.62 + removed / Math.max(20, title.length)));
  return { key, confidence: beforeGeneric === normalizeText(title) ? 0.55 : confidence };
}
