/**
 * The same schedule written in each syntax. Shared by the unit tests (conversion must map every form
 * to every other) and the e2e suite (the server must fire each form at identical times).
 * `null` means that syntax cannot say it, so a conversion to it must be refused with a warning.
 */
export interface Meaning {
  name: string;
  natural: string | null;
  cron: string | null;
  quartz: string | null;
  rrule: string | null;
}

export const MEANINGS: Meaning[] = [
  { name: "every minute", natural: "every minute", cron: "* * * * *", quartz: "0 * * * * ?", rrule: "FREQ=MINUTELY;INTERVAL=1" },
  { name: "every 5 minutes", natural: "every 5 minutes", cron: "*/5 * * * *", quartz: "0 */5 * * * ?", rrule: "FREQ=MINUTELY;INTERVAL=5" },
  { name: "every 15 minutes", natural: "every 15 minutes", cron: "*/15 * * * *", quartz: "0 */15 * * * ?", rrule: "FREQ=MINUTELY;INTERVAL=15" },
  { name: "every 30 minutes", natural: "every 30 minutes", cron: "*/30 * * * *", quartz: "0 */30 * * * ?", rrule: "FREQ=MINUTELY;INTERVAL=30" },
  { name: "every hour", natural: "every hour", cron: "0 * * * *", quartz: "0 0 * * * ?", rrule: "FREQ=HOURLY;INTERVAL=1;BYMINUTE=0" },
  { name: "every 6 hours", natural: "every 6 hours", cron: "0 */6 * * *", quartz: "0 0 */6 * * ?", rrule: "FREQ=HOURLY;INTERVAL=6;BYMINUTE=0" },
  { name: "every 12 hours", natural: "every 12 hours", cron: "0 */12 * * *", quartz: "0 0 */12 * * ?", rrule: "FREQ=HOURLY;INTERVAL=12;BYMINUTE=0" },
  // Natural has no minute offset for hourly rules.
  { name: "hourly at :30", natural: null, cron: "30 * * * *", quartz: "0 30 * * * ?", rrule: "FREQ=HOURLY;INTERVAL=1;BYMINUTE=30" },
  { name: "every 4 hours at :15", natural: null, cron: "15 */4 * * *", quartz: "0 15 */4 * * ?", rrule: "FREQ=HOURLY;INTERVAL=4;BYMINUTE=15" },
  { name: "daily at midnight", natural: "every day at 00:00", cron: "0 0 * * *", quartz: "0 0 0 * * ?", rrule: "FREQ=DAILY;BYHOUR=0;BYMINUTE=0" },
  { name: "daily at 09:00", natural: "every day at 09:00", cron: "0 9 * * *", quartz: "0 0 9 * * ?", rrule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=0" },
  { name: "daily at 23:59", natural: "every day at 23:59", cron: "59 23 * * *", quartz: "0 59 23 * * ?", rrule: "FREQ=DAILY;BYHOUR=23;BYMINUTE=59" },
  { name: "daily at 02:30 (DST gap hour)", natural: "every day at 02:30", cron: "30 2 * * *", quartz: "0 30 2 * * ?", rrule: "FREQ=DAILY;BYHOUR=2;BYMINUTE=30" },
  { name: "weekdays at 09:00", natural: "every weekday at 09:00", cron: "0 9 * * 1-5", quartz: "0 0 9 ? * MON-FRI", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0" },
  { name: "weekends at 10:00", natural: "every weekend at 10:00", cron: "0 10 * * 0,6", quartz: "0 0 10 ? * SAT,SUN", rrule: "FREQ=WEEKLY;BYDAY=SU,SA;BYHOUR=10;BYMINUTE=0" },
  { name: "Mondays at 08:00", natural: "every monday at 08:00", cron: "0 8 * * 1", quartz: "0 0 8 ? * MON", rrule: "FREQ=WEEKLY;BYDAY=MO;BYHOUR=8;BYMINUTE=0" },
  { name: "Sundays at 00:00", natural: "every sunday at 00:00", cron: "0 0 * * 0", quartz: "0 0 0 ? * SUN", rrule: "FREQ=WEEKLY;BYDAY=SU;BYHOUR=0;BYMINUTE=0" },
  { name: "Saturdays at 23:45", natural: "every saturday at 23:45", cron: "45 23 * * 6", quartz: "0 45 23 ? * SAT", rrule: "FREQ=WEEKLY;BYDAY=SA;BYHOUR=23;BYMINUTE=45" },
  { name: "Mondays and Thursdays at 10:00", natural: "every monday and thursday at 10:00", cron: "0 10 * * 1,4", quartz: "0 0 10 ? * MON,THU", rrule: "FREQ=WEEKLY;BYDAY=MO,TH;BYHOUR=10;BYMINUTE=0" },
  { name: "Mon, Wed, Fri at 08:30", natural: "every monday and wednesday and friday at 08:30", cron: "30 8 * * 1,3,5", quartz: "0 30 8 ? * MON,WED,FRI", rrule: "FREQ=WEEKLY;BYDAY=MO,WE,FR;BYHOUR=8;BYMINUTE=30" },
  { name: "Sunday and Monday at 06:00", natural: "every sunday and monday at 06:00", cron: "0 6 * * 0,1", quartz: "0 0 6 ? * SUN,MON", rrule: "FREQ=WEEKLY;BYDAY=SU,MO;BYHOUR=6;BYMINUTE=0" },
  { name: "1st of the month at 09:00", natural: "on the 1st of every month at 09:00", cron: "0 9 1 * *", quartz: "0 0 9 1 * ?", rrule: "FREQ=MONTHLY;BYMONTHDAY=1;BYHOUR=9;BYMINUTE=0" },
  { name: "1st and 15th at 08:00", natural: "on the 1st and 15th of every month at 08:00", cron: "0 8 1,15 * *", quartz: "0 0 8 1,15 * ?", rrule: "FREQ=MONTHLY;BYMONTHDAY=1,15;BYHOUR=8;BYMINUTE=0" },
  { name: "2nd, 3rd and 22nd at 12:00", natural: "on the 2nd and 3rd and 22nd of every month at 12:00", cron: "0 12 2,3,22 * *", quartz: "0 0 12 2,3,22 * ?", rrule: "FREQ=MONTHLY;BYMONTHDAY=2,3,22;BYHOUR=12;BYMINUTE=0" },
  { name: "31st at 23:00 (short months skip)", natural: "on the 31st of every month at 23:00", cron: "0 23 31 * *", quartz: "0 0 23 31 * ?", rrule: "FREQ=MONTHLY;BYMONTHDAY=31;BYHOUR=23;BYMINUTE=0" },
  // Steps that restart each hour or day mean something different in cron than "every N".
  { name: "every 7 minutes", natural: "every 7 minutes", cron: null, quartz: null, rrule: "FREQ=MINUTELY;INTERVAL=7" },
  { name: "every 90 minutes", natural: "every 90 minutes", cron: null, quartz: null, rrule: "FREQ=MINUTELY;INTERVAL=90" },
  { name: "every 5 hours", natural: "every 5 hours", cron: null, quartz: null, rrule: "FREQ=HOURLY;INTERVAL=5;BYMINUTE=0" },
];

export const SYNTAXES = ["natural", "cron", "quartz", "rrule"] as const;
export type Syntax = (typeof SYNTAXES)[number];

// Hand-written inputs, each paired with its exact conversion (cron → natural, quartz → cron,
// rrule → cron, natural → cron). The e2e suite also asks the real server whether each one fires
// at the same times as its source.

export const CRON_TO_NATURAL: [string, string][] = [
    ["*/1 * * * *", "every minute"],
    ["0 9 * * MON-FRI", "every weekday at 09:00"],
    ["0 9 * * mon-fri", "every weekday at 09:00"],
    ["0 9 * * 1,2,3,4,5", "every weekday at 09:00"],
    ["0 9 * * 5,4,3,2,1", "every weekday at 09:00"],
    ["0 9 * * MON,WED,FRI", "every monday and wednesday and friday at 09:00"],
    ["0 9 * * 7", "every sunday at 09:00"],
    ["0 9 * * 0,7", "every sunday at 09:00"],
    ["0 9 * * SAT,SUN", "every weekend at 09:00"],
    ["0 9 1-5 * *", "on the 1st and 2nd and 3rd and 4th and 5th of every month at 09:00"],
    ["0 9 11,12,13,21,22,23 * *", "on the 11th and 12th and 13th and 21st and 22nd and 23rd of every month at 09:00"],
    ["00 09 * * *", "every day at 09:00"],
    ["@hourly", "every hour"],
    ["@daily", "every day at 00:00"],
    ["@midnight", "every day at 00:00"],
    ["@weekly", "every sunday at 00:00"],
    ["@monthly", "on the 1st of every month at 00:00"],
    ["@DAILY", "every day at 00:00"],
    ["0 0 9 * * *", "every day at 09:00"], // leading seconds field
    ["0 */5 * * * *", "every 5 minutes"],
    ["0 0 12 * * 1-5", "every weekday at 12:00"],
    ["*/2 * * * *", "every 2 minutes"],
    ["0 */2 * * *", "every 2 hours"],
    ["0 */24 * * *", "every 24 hours"], // 24 divides a day, so the step is exact
  ];

export const QUARTZ_TO_CRON: [string, string][] = [
    ["0 0 9 ? * MON-FRI", "0 9 * * 1-5"],
    ["0 0 9 * * ?", "0 9 * * *"],
    ["0 0 9 ? * *", "0 9 * * *"],
    ["0 0 9 * * *", "0 9 * * *"], // cron-with-seconds spelling; same meaning here
    ["0 0 9 ? * 2-6", "0 9 * * 1-5"], // numbered from Sunday = 1
    ["0 0 9 ? * 1", "0 9 * * 0"],
    ["0 0 9 ? * 7", "0 9 * * 6"],
    ["0 0 9 ? * 1,7", "0 9 * * 0,6"],
    ["0 0 9 ? * mon,fri", "0 9 * * 1,5"],
    ["0 0 9 1,15 * ?", "0 9 1,15 * *"],
    ["0 0 9 15 * ?", "0 9 15 * *"],
    ["0 0/5 * * * ?", "*/5 * * * *"],
    ["0 */5 * * * ?", "*/5 * * * *"],
    ["0 0 0/6 * * ?", "0 */6 * * *"],
    ["0 30 * * * ?", "30 * * * *"],
    ["0 0 9 * * ? *", "0 9 * * *"], // year *
  ];

export const RRULE_TO_CRON: [string, string][] = [
    ["FREQ=DAILY;BYHOUR=9;BYMINUTE=0", "0 9 * * *"],
    ["RRULE:FREQ=DAILY;BYHOUR=9;BYMINUTE=0", "0 9 * * *"],
    ["rrule:freq=daily;byhour=9;byminute=0", "0 9 * * *"],
    ["freq=daily;byhour=9;byminute=0", "0 9 * * *"],
    ["FREQ=DAILY;BYMINUTE=0;BYHOUR=9", "0 9 * * *"], // order doesn't matter
    ["FREQ=DAILY;INTERVAL=1;BYHOUR=9;BYMINUTE=0", "0 9 * * *"],
    ["FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0", "0 9 * * *"],
    ["FREQ=MINUTELY", "* * * * *"],
    ["FREQ=MINUTELY;INTERVAL=5", "*/5 * * * *"],
    ["FREQ=HOURLY;BYMINUTE=0", "0 * * * *"],
    ["FREQ=HOURLY;INTERVAL=3;BYMINUTE=20", "20 */3 * * *"],
    ["FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0", "0 9 * * 1-5"],
    ["FREQ=WEEKLY;BYDAY=FR,MO;BYHOUR=9;BYMINUTE=0", "0 9 * * 1,5"],
    ["FREQ=WEEKLY;BYDAY=SU;BYHOUR=9;BYMINUTE=0", "0 9 * * 0"],
    ["FREQ=MONTHLY;BYMONTHDAY=1,15;BYHOUR=9;BYMINUTE=0", "0 9 1,15 * *"],
    ["FREQ=MONTHLY;BYMONTHDAY=31;BYHOUR=0;BYMINUTE=0;BYSECOND=0", "0 0 31 * *"],
  ];

export const NATURAL_TO_CRON: [string, string][] = [
    ["every minute", "* * * * *"],
    ["every 1 minute", "* * * * *"],
    ["every minutes", "* * * * *"],
    ["EVERY 5 MINUTES", "*/5 * * * *"],
    ["every   5   minutes", "*/5 * * * *"],
    ["  every 5 minutes  ", "*/5 * * * *"],
    ["every hour", "0 * * * *"],
    ["every 1 hour", "0 * * * *"],
    ["hourly", "0 * * * *"],
    ["Hourly", "0 * * * *"],
    ["every 3 hours", "0 */3 * * *"],
    ["every day at 9am", "0 9 * * *"],
    ["daily at 9am", "0 9 * * *"],
    ["every day at 09:00", "0 9 * * *"],
    ["every day at 9:05", "5 9 * * *"],
    ["every day at 9:05am", "5 9 * * *"],
    ["every day at 9:05 pm", "5 21 * * *"],
    ["every day at 12am", "0 0 * * *"],
    ["every day at 12:30am", "30 0 * * *"],
    ["every day at 12pm", "0 12 * * *"],
    ["every day at 12:59pm", "59 12 * * *"],
    ["every day at 1pm", "0 13 * * *"],
    ["every day at 11:59pm", "59 23 * * *"],
    ["every day at 23:59", "59 23 * * *"],
    ["every day at 0:00", "0 0 * * *"],
    ["every day at noon", "0 12 * * *"],
    ["every day at midnight", "0 0 * * *"],
    ["every weekday at 6:30pm", "30 18 * * 1-5"],
    ["every weekdays at 6:30pm", "30 18 * * 1-5"],
    ["every weekend at 10:00", "0 10 * * 0,6"],
    ["every weekends at 10:00", "0 10 * * 0,6"],
    ["every monday at 8am", "0 8 * * 1"],
    ["every Monday at 8AM", "0 8 * * 1"],
    ["every monday and thursday at 10:00", "0 10 * * 1,4"],
    ["every thursday and monday at 10:00", "0 10 * * 1,4"],
    ["every monday, wednesday and friday at 9am", "0 9 * * 1,3,5"],
    ["every monday, wednesday, friday at 9am", "0 9 * * 1,3,5"],
    ["every monday and monday at 9am", "0 9 * * 1"],
    ["every sunday at 9am", "0 9 * * 0"],
    ["every saturday and sunday at 9am", "0 9 * * 0,6"],
    ["on the 1st of every month at 9am", "0 9 1 * *"],
    ["on the 1st and 15th of every month at 8am", "0 8 1,15 * *"],
    ["on the 1st, 15th and 28th of every month at 8am", "0 8 1,15,28 * *"],
    ["on the 2nd and 3rd of every month at 8am", "0 8 2,3 * *"],
    ["on the 11th, 12th and 13th of every month at 8am", "0 8 11,12,13 * *"],
    ["on the 15th and 1st of every month at 8am", "0 8 1,15 * *"],
    ["on the 31st of every month at 11pm", "0 23 31 * *"],
  ];
