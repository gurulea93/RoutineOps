import { test, expect, Page } from '@playwright/test'

const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@mdm.local'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin'

async function loginOnce(page: Page) {
  await page.goto('/login')
  await page.fill('#email', EMAIL)
  await page.fill('#password', PASSWORD)
  await page.click('button[type=submit]')
  await page.waitForURL('**/devices')
}

test.describe('smoke', () => {
  test.beforeEach(async ({ page }) => {
    await loginOnce(page)
  })

  const routes = [
    '/devices',
    '/alerts',
    '/admin-access',
    '/policies',
    '/scripts',
    '/script-policies',
    '/audit-log',
  ]

  for (const route of routes) {
    test(`${route} loads`, async ({ page }) => {
      await page.goto(route)
      await expect(page).not.toHaveURL('/login')
      await expect(page.locator('h1').first()).toBeVisible()
    })
  }

  test('login redirect works', async ({ page }) => {
    await page.evaluate(() => localStorage.clear())
    await page.goto('/devices')
    await expect(page).toHaveURL(/\/login/)
  })
})