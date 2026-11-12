import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import SIWCUsageCell from '../SIWCUsageCell.vue'
import UsageProgressBar from '../UsageProgressBar.vue'
import type { AccountUsageInfo } from '@/types'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string, args?: {cost: string}) => args?.cost ? `预计总额 $${args.cost}` : key }) }))
const usage = (used = 4): AccountUsageInfo => ({
 updated_at: '2026-11-12T12:00:00Z',
 seven_day: { utilization: used, resets_at: '2099-11-19T12:00:00Z', window_stats: { requests: 12, tokens: 1234, cost: 1.2345, user_cost: 2.5 } }
} as AccountUsageInfo)

describe('SIWC ordinary usage layout', () => {
 it('renders real 7d stats, raw precision estimate and missing 5h', () => {
  const w=mount(SIWCUsageCell,{props:{usage:usage()}})
  expect(w.findAllComponents(UsageProgressBar)).toHaveLength(1)
  expect(w.text()).toContain('5h：未提供')
  expect(w.text()).toContain('12 req');expect(w.text()).toContain('A $1.23');expect(w.text()).toContain('U $2.50')
  expect(w.text()).toContain('预计总额 $30.86')
  expect(w.text()).not.toContain('SIWC 额度');w.unmount()
 })
 it('shows dual windows, 0% unavailable estimate and zero stats without fake 5h', async () => {
  const data=usage(0);data.five_hour={utilization:3,resets_at:'2099-11-12T15:00:00Z'}
  data.seven_day!.window_stats={requests:0,tokens:0,cost:0,user_cost:0}
  const w=mount(SIWCUsageCell,{props:{usage:data}})
  expect(w.findAllComponents(UsageProgressBar)).toHaveLength(2)
  expect(w.text()).toContain('0 req');expect(w.text()).toContain('暂不可估');expect(w.text()).not.toContain('未提供')
  await w.get('button').trigger('click');expect(w.emitted('refresh')).toHaveLength(1);w.unmount()
 })
 it('keeps full previous snapshot on failure and clears on account/page reset',async()=>{
  const data=usage();data.error='HTTP 401'
  const w=mount(SIWCUsageCell,{props:{usage:data}})
  expect(w.text()).toContain('上次数据');expect(w.text()).toContain('预计总额 $30.86')
  await w.setProps({usage:null,loading:true})
  expect(w.findAllComponents(UsageProgressBar)).toHaveLength(0);expect(w.text()).not.toContain('30.86');expect(w.get('button').attributes('disabled')).toBeDefined();w.unmount()
 })
 it('does not estimate an expired or missing-stat window',()=>{
  const data=usage();data.seven_day!.resets_at='2000-01-01T00:00:00Z';data.seven_day!.window_stats=undefined
  const w=mount(SIWCUsageCell,{props:{usage:data}})
  expect(w.text()).toContain('暂不可估');expect(w.text()).not.toContain('A $');w.unmount()
 })
})
