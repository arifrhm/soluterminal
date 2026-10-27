# DeepSeek pricing data maintenance

`deepseekusage` is the canonical SoluTerminal source for DeepSeek rate and
schedule decisions. Values displayed by the UI and live DSH estimates are CNY
per 1 million tokens. Historical provider CSV `price`, `cost`, and currency
fields stay authoritative; this package never performs an FX conversion.

Pricing was verified on 2026-09-29 against both official pages:

- English: https://api-docs.deepseek.com/quick_start/pricing/
- Chinese/CNY: https://api-docs.deepseek.com/zh-cn/quick_start/pricing/

The maintained public-holiday calendar comes from the State Council notices:

- 2025 (issued 2024-11-12): https://www.gov.cn/zhengce/content/202411/content_6986383.htm
- 2026 (issued 2025-11-04): https://www.gov.cn/zhengce/zhengceku/202511/content_7047091.htm

Calendar dates and the documented Beijing-time windows are evaluated in
`Asia/Shanghai`. Coverage is explicitly limited to 2025–2026. Outside it,
callers must display schedule-only/holiday-unverified status and must not quote
a guaranteed current rate, discount, transition, or cost.

To add a year after its official notice is published:

1. Verify the DeepSeek English and Chinese pricing pages and update the rate
   verification date if necessary.
2. Add every closure date from the official State Council notice explicitly to
   `calendar.go`, then add its URL and issue date to `calendarSources`.
3. Extend `CalendarCoverageTo`, bump `CalendarVersion` and
   `CalendarUpdatedDate`, and add boundary, consecutive-closure, weekend and
   expiry tests.
4. Run the shared, daemon and web suites.

Never guess lunar-calendar dates. Chinese make-up work Saturdays/Sundays remain
off-peak under DeepSeek's currently documented Monday–Friday policy; do not
treat them as peak unless DeepSeek explicitly changes that policy.
