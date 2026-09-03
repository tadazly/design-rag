# DRAG_PLUGIN_E2E_20260901 验收活动

## 玩法流程

玩家每日完成三次巡逻，获得齿轮券；每五张齿轮券可启动一次验收扭蛋机。十次抽取内必定获得一枚金色齿轮，领取后本轮保底计数清零。

## 产出逻辑

普通奖励从 `dragAcceptanceRewardPool` 的当前奖池随机抽取；金色齿轮由 `dragAcceptanceGuarantee` 的 `pityCount=10` 控制。所有奖励最终通过 `drop` 表中的掉落包发放。

## 配表

- `dragAcceptanceActivity`：活动时间、入口和版本开关。
- `dragAcceptanceRewardPool`：奖池、权重和展示顺序。
- `dragAcceptanceGuarantee`：保底次数与重置规则。
- `drop`：最终奖励掉落包。
