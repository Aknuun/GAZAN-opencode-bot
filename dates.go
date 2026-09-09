package main

import (
	"time"

	ptime "github.com/yaa110/go-persian-calendar"
)

// tehranLoc منطقهٔ زمانی ایران (UTC+3:30 ثابت؛ ایران ساعت تابستانی ندارد)
var tehranLoc = time.FixedZone("Iran", 3*3600+30*60)

var faMonths = [...]string{
	"فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور",
	"مهر", "آبان", "آذر", "دی", "بهمن", "اسفند",
}

// createdTimeLabel زمان ساخت نشست (epoch میلی‌ثانیه) را به «روز ماه ساعت:دقیقه» شمسیِ ایران تبدیل می‌کند
func createdTimeLabel(ms int64, now time.Time) string {
	t := ptime.New(time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond)).In(tehranLoc))
	label := faNum(t.Day()) + " " + faMonths[t.Month()-1]
	if cur := ptime.New(now.In(tehranLoc)); t.Year() != cur.Year() {
		label += " " + faNum(t.Year())
	}
	return label + " " + faClock(t.Hour()) + ":" + faClock(t.Minute())
}

// faClock عدد ۰..۵۹ را به شکل دو رقمیِ فارسی برمی‌گرداند
func faClock(n int) string {
	if n < 10 {
		return "۰" + faNum(n)
	}
	return faNum(n)
}
