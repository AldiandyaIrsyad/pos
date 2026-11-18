const rupiah = new Intl.NumberFormat('id-ID', {
  style: 'currency',
  currency: 'IDR',
  maximumFractionDigits: 0,
})

export const formatRupiah = (n: number) => rupiah.format(n)

const number = new Intl.NumberFormat('id-ID')

export const formatNumber = (n: number) => number.format(n)

const day = new Intl.DateTimeFormat('id-ID', { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })

// "2026-01-05" -> "Sen, 5 Jan 2026". Parsed as a calendar date, not shifted by time zone.
export const formatDay = (isoDate: string) => {
  const [y, m, d] = isoDate.split('-').map(Number)
  return day.format(new Date(y, m - 1, d))
}
