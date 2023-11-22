/*
 * Tencent is pleased to support the open source community by making BK-CI 蓝鲸持续集成平台 available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company.  All rights reserved.
 *
 * BK-CI 蓝鲸持续集成平台 is licensed under the MIT license.
 *
 * A copy of the MIT License is included in this file.
 *
 *
 * Terms of the MIT License:
 * ---------------------------------------------------
 * Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
 * documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all copies or substantial portions of
 * the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT
 * LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN
 * NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
 * WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 *
 */

package com.tencent.devops.process.engine.dao

import com.tencent.devops.model.process.tables.TPipelineYamlInfo
import com.tencent.devops.model.process.tables.records.TPipelineYamlInfoRecord
import com.tencent.devops.process.pojo.pipeline.PipelineYamlInfo
import org.jooq.DSLContext
import org.springframework.stereotype.Repository
import java.time.LocalDateTime

/**
 * 流水线与代码库yml文件关联表
 */
@Repository
class PipelineYamlInfoDao {

    fun save(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
        filePath: String,
        pipelineId: String,
        userId: String
    ) {
        val now = LocalDateTime.now()
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            dslContext.insertInto(
                this,
                PROJECT_ID,
                REPO_HASH_ID,
                FILE_PATH,
                PIPELINE_ID,
                CREATOR,
                MODIFIER,
                CREATE_TIME,
                UPDATE_TIME
            ).values(
                projectId,
                repoHashId,
                filePath,
                pipelineId,
                userId,
                userId,
                now,
                now
            ).onDuplicateKeyIgnore()
                .execute()
        }
    }

    fun update(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
        filePath: String,
        userId: String
    ) {
        val now = LocalDateTime.now()
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            dslContext.update(this)
                .set(MODIFIER, userId)
                .set(UPDATE_TIME, now)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .and(FILE_PATH.eq(filePath))
                .execute()
        }
    }

    fun get(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
        filePath: String
    ): PipelineYamlInfo? {
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            val record = dslContext.selectFrom(this)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .and(FILE_PATH.eq(filePath))
                .fetchOne()
            return record?.let { convert(it) }
        }
    }

    fun get(
        dslContext: DSLContext,
        projectId: String,
        pipelineId: String
    ): PipelineYamlInfo? {
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            val record = dslContext.selectFrom(this)
                .where(PROJECT_ID.eq(projectId))
                .and(PIPELINE_ID.eq(pipelineId))
                .fetchOne()
            return record?.let { convert(it) }
        }
    }

    fun listPipelineIdWithFolder(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
        folder: String?
    ): List<String> {
        return with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            dslContext.select(PIPELINE_ID).from(this)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .let { if (folder == null) it else it.and(FILE_PATH.like(".ci/$folder/%")) }
                .fetch().map { it.value1() }
        }
    }

    fun getAllByRepo(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String
    ): List<PipelineYamlInfo> {
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            return dslContext.selectFrom(this)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .fetch {
                    convert(it)
                }
        }
    }

    fun delete(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
        filePath: String
    ) {
        with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            dslContext.deleteFrom(this)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .and(FILE_PATH.eq(filePath))
                .execute()
        }
    }

    fun countYamlPipeline(
        dslContext: DSLContext,
        projectId: String,
        repoHashId: String,
    ): Long {
        return with(TPipelineYamlInfo.T_PIPELINE_YAML_INFO) {
            dslContext.selectCount().from(this)
                .where(PROJECT_ID.eq(projectId))
                .and(REPO_HASH_ID.eq(repoHashId))
                .and(DELETE.eq(false))
                .fetchOne(0, Long::class.java) ?: 0L
        }
    }

    fun convert(record: TPipelineYamlInfoRecord): PipelineYamlInfo {
        return with(record) {
            PipelineYamlInfo(
                projectId = projectId,
                repoHashId = repoHashId,
                filePath = filePath,
                pipelineId = pipelineId,
                creator = creator,
                delete = delete
            )
        }
    }
}
